package controller_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/resourceutil"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const expectedPromiseVersion = "v2.0.0"

var _ = Describe("HealthRecordController", func() {
	var (
		now           int64
		healthRecord  *v1alpha1.HealthRecord
		promise       *v1alpha1.Promise
		resource      *unstructured.Unstructured
		details       *runtime.RawExtension
		reconciler    *controller.HealthRecordReconciler
		eventRecorder *events.FakeRecorder
	)

	reconcile := func() *unstructured.Unstructured {
		result, err := t.reconcileUntilCompletion(reconciler, healthRecord)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		updatedResource := &unstructured.Unstructured{}
		updatedResource.SetKind(resource.GetKind())
		updatedResource.SetAPIVersion(resource.GetAPIVersion())

		err = fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)
		Expect(err).ToNot(HaveOccurred())

		return updatedResource
	}

	BeforeEach(func() {
		promise = createPromise(promisePath)
		resource = createResourceRequest()

		details = &runtime.RawExtension{Raw: []byte(`{"info":"message"}`)}

		now = time.Now().Unix()
		healthRecord = &v1alpha1.HealthRecord{
			TypeMeta: metav1.TypeMeta{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "HealthRecord",
			},
			ObjectMeta: metav1.ObjectMeta{Name: "a-name", Namespace: "default"},
			Data: v1alpha1.HealthRecordData{
				PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
				ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
				State:       "ready",
				LastRun:     now,
				Details:     details,
			},
		}

		eventRecorder = events.NewFakeRecorder(1024)

		reconciler = &controller.HealthRecordReconciler{
			Client:        fakeK8sClient,
			Scheme:        scheme.Scheme,
			Log:           GinkgoLogr,
			EventRecorder: eventRecorder,
		}

		Expect(fakeK8sClient.Create(ctx, healthRecord)).To(Succeed())
	})

	When("there is a single healthRecord", func() {
		When("reconciling against a resource request", func() {
			var updatedResource *unstructured.Unstructured

			BeforeEach(func() {
				updatedResource = reconcile()
			})

			It("updates the resource status.healthStatus with the healthRecord data", func() {
				status := getResourceStatus(updatedResource)
				Expect(status).To(HaveKey("healthStatus"))
				Expect(getHealthStatusState(status)).To(Equal("ready"))

				records := getHealthRecordsList(status)
				Expect(records[0]).To(HaveKeyWithValue("lastRun", healthRecord.Data.LastRun))
				Expect(records[0]).To(HaveKeyWithValue("state", healthRecord.Data.State))
				Expect(records[0]).To(HaveKeyWithValue("details", HaveKeyWithValue("info", "message")))
				Expect(records[0]).To(HaveKeyWithValue("source", HaveKeyWithValue("name", healthRecord.GetName())))
				Expect(records[0]).To(HaveKeyWithValue("source", HaveKeyWithValue("namespace", healthRecord.GetNamespace())))
				Expect(records[0]).NotTo(HaveKey("promiseVersion"))
			})

			DescribeTable("firing events detailing the healthStatus state",
				func(state string, eventMessage string) {
					Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
					updatedResource = reconcile()

					healthRecord = &v1alpha1.HealthRecord{
						TypeMeta: metav1.TypeMeta{
							APIVersion: v1alpha1.GroupVersion.String(),
							Kind:       "HealthRecord",
						},
						ObjectMeta: metav1.ObjectMeta{Name: "a-name", Namespace: "default"},
						Data: v1alpha1.HealthRecordData{
							PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
							ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
							State:       state,
							LastRun:     now,
							Details:     details,
						},
					}

					Expect(fakeK8sClient.Create(ctx, healthRecord)).To(Succeed())
					updatedResource = reconcile()

					Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
						eventMessage)))
				},
				Entry("When the state is 'unknown'", "unknown", "Warning HealthRecord Health state is unknown"),
				Entry("When the state is 'unhealthy'", "unhealthy", "Warning HealthRecord Health state is unhealthy"),
				Entry("When the state is 'degraded'", "degraded", "Warning HealthRecord Health state is degraded"),
				Entry("When the state is 'healthy'", "healthy", "Normal HealthRecord Health state is healthy"),
				Entry("When the state is 'ready'", "ready", "Normal HealthRecord Health state is ready"),
			)
		})

		When("the resource request status has fields other than the healthStatus field", func() {
			BeforeEach(func() {
				statusMap := map[string]interface{}{
					"some": "status",
					"nested": map[string]interface{}{
						"value": "data",
					},
				}
				Expect(unstructured.SetNestedMap(resource.Object, statusMap, "status")).To(Succeed())
				Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())
			})

			It("doesn't overwrite the existing status keys", func() {
				updatedResource := reconcile()

				status := getResourceStatus(updatedResource)

				Expect(status).To(SatisfyAll(
					HaveKeyWithValue("some", "status"),
					HaveKeyWithValue("nested", HaveKeyWithValue("value", "data")),
					HaveKeyWithValue("healthStatus", HaveKeyWithValue("state", healthRecord.Data.State)),
				))
			})
		})

		When("the resource request healthStatus already carries what the status-writer wrote", func() {
			BeforeEach(func() {
				statusMap := map[string]any{
					"healthStatus": map[string]any{
						"state":                  "unknown",
						"expectedPromiseVersion": "v2.0.0",
						"healthDefinitions":      int64(1),
					},
				}
				Expect(unstructured.SetNestedMap(resource.Object, statusMap, "status")).To(Succeed())
				Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())
			})

			It("keeps those fields when it recomputes the state", func() {
				updatedResource := reconcile()

				healthStatus := getResourceHealthStatus(updatedResource)
				Expect(healthStatus).To(SatisfyAll(
					HaveKeyWithValue("expectedPromiseVersion", "v2.0.0"),
					HaveKeyWithValue("healthDefinitions", int64(1)),
					HaveKeyWithValue("state", healthRecord.Data.State),
				))
			})

			It("keeps those fields when the last record is deleted", func() {
				reconcile()
				Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
				_, err := t.reconcileUntilCompletion(reconciler, healthRecord)
				Expect(err).NotTo(HaveOccurred())

				updatedResource := &unstructured.Unstructured{}
				updatedResource.SetGroupVersionKind(resource.GroupVersionKind())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())
				Expect(getResourceHealthStatus(updatedResource)).To(SatisfyAll(
					HaveKeyWithValue("expectedPromiseVersion", "v2.0.0"),
					HaveKeyWithValue("healthDefinitions", int64(1)),
				))
			})
		})

		When("the resource request HealthStatus already has a HealthRecord with a matching state", func() {
			BeforeEach(func() {
				now = time.Now().Unix()
				healthRecord.Data.State = "healthy"
				healthRecord.Data.LastRun = now
				Expect(fakeK8sClient.Update(ctx, healthRecord)).To(Succeed())
			})

			It("updates the run time of the existing HealthRecord", func() {
				updatedResource := reconcile()
				status := getResourceStatus(updatedResource)

				Expect(getHealthStatusState(status)).To(Equal("healthy"))
				records := getHealthRecordsList(status)

				Expect(records[0]).To(SatisfyAll(
					HaveKeyWithValue("state", healthRecord.Data.State),
					HaveKeyWithValue("details", HaveKeyWithValue("info", "message")),
					HaveKeyWithValue("lastRun", now),
				))
			})

			It("does not fire an event detailing the healthRecord state", func() {
				Eventually(eventRecorder.Events).ShouldNot(Receive(ContainSubstring(
					"Normal HealthRecord Health state is ready")))
			})
		})
	})

	When("there are multiple healthRecords for a single resource", func() {
		DescribeTable("the state of the request healthStatus is calculated accordingly",
			func(state string, expectedState string) {
				details = &runtime.RawExtension{Raw: []byte(`{"furtherInfo":"present"}`)}

				now = time.Now().Unix()
				healthRecord = &v1alpha1.HealthRecord{
					TypeMeta: metav1.TypeMeta{
						APIVersion: v1alpha1.GroupVersion.String(),
						Kind:       "HealthRecord",
					},
					ObjectMeta: metav1.ObjectMeta{Name: "b-name", Namespace: "default"},
					Data: v1alpha1.HealthRecordData{
						PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
						ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
						State:       state,
						LastRun:     now,
						Details:     details,
					},
				}

				Expect(fakeK8sClient.Create(ctx, healthRecord)).To(Succeed())
				updatedResource := reconcile()

				status := getResourceStatus(updatedResource)
				statusState := getHealthStatusState(status)
				records := getHealthRecordsList(status)

				Expect(records).To(HaveLen(2))
				Expect(statusState).To(Equal(expectedState))
			},

			Entry("it is healthy when one of the healthRecords is healthy", "healthy", "healthy"),
			Entry("it is unhealthy when one of the healthRecords is unhealthy", "unhealthy", "unhealthy"),
			Entry("it is degraded when one of the healthRecords is degraded", "degraded", "degraded"),
			Entry("it is unknown when one of the healthRecords is unknown", "unknown", "unknown"),
		)
	})

	When("the healthRecords for a resource are listed in an arbitrary order", func() {
		createRecordForResource := func(namespace, name string) {
			record := &v1alpha1.HealthRecord{
				TypeMeta: metav1.TypeMeta{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "HealthRecord",
				},
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Data: v1alpha1.HealthRecordData{
					PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
					ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
					State:       "ready",
					LastRun:     now,
					Details:     details,
				},
			}
			Expect(fakeK8sClient.Create(ctx, record)).To(Succeed())
		}

		BeforeEach(func() {
			reorderListResults = func(list client.ObjectList) {
				records, ok := list.(*v1alpha1.HealthRecordList)
				if !ok {
					return
				}
				slices.Reverse(records.Items)
			}
		})

		It("orders the healthRecords by source name", func() {
			createRecordForResource("default", "z-name")
			createRecordForResource("default", "b-name")

			status := getResourceStatus(reconcile())

			Expect(getHealthRecordSources(status)).To(Equal([]string{
				"default/a-name",
				"default/b-name",
				"default/z-name",
			}))
		})

		It("orders the healthRecords by source namespace before source name", func() {
			createRecordForResource("zzz-ns", "a-name")
			createRecordForResource("aaa-ns", "z-name")

			status := getResourceStatus(reconcile())

			Expect(getHealthRecordSources(status)).To(Equal([]string{
				"aaa-ns/z-name",
				"default/a-name",
				"zzz-ns/a-name",
			}))
		})
	})

	When("there are healthRecords for other resources", func() {
		BeforeEach(func() {
			otherHealthRecord := &v1alpha1.HealthRecord{
				TypeMeta: metav1.TypeMeta{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "HealthRecord",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "other-name", Namespace: "default"},
				Data: v1alpha1.HealthRecordData{
					PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
					ResourceRef: v1alpha1.ResourceRef{Name: "other-resource", Namespace: resource.GetNamespace()},
					State:       "unhealthy",
					LastRun:     now,
					Details:     details,
				},
			}

			Expect(fakeK8sClient.Create(ctx, otherHealthRecord)).To(Succeed())
		})

		It("only considers records with matching resourceRef", func() {
			updatedResource := reconcile()

			status := getResourceStatus(updatedResource)
			statusState := getHealthStatusState(status)
			records := getHealthRecordsList(status)

			Expect(records).To(HaveLen(1))
			Expect(statusState).To(Equal("ready"))
		})
	})

	When("another promise has a resource with the same name and namespace", func() {
		BeforeEach(func() {
			otherPromiseRecord := &v1alpha1.HealthRecord{
				TypeMeta: metav1.TypeMeta{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "HealthRecord",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "other-promise-record", Namespace: "default"},
				Data: v1alpha1.HealthRecordData{
					PromiseRef:  v1alpha1.PromiseRef{Name: "other-promise"},
					ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
					State:       "unhealthy",
					LastRun:     now,
					Details:     details,
				},
			}

			Expect(fakeK8sClient.Create(ctx, otherPromiseRecord)).To(Succeed())
		})

		It("only considers records with a matching promiseRef", func() {
			updatedResource := reconcile()

			status := getResourceStatus(updatedResource)
			records := getHealthRecordsList(status)

			Expect(records).To(HaveLen(1))
			Expect(records[0]).To(HaveKeyWithValue("source", HaveKeyWithValue("name", healthRecord.GetName())))
			Expect(getHealthStatusState(status)).To(Equal("ready"))
		})
	})

	When("a healthRecord is deleted", func() {
		var updatedResource *unstructured.Unstructured

		BeforeEach(func() {
			updatedResource = reconcile()
		})

		It("succeeds", func() {
			healthRecordName := types.NamespacedName{
				Name:      healthRecord.GetName(),
				Namespace: healthRecord.GetNamespace(),
			}

			fakeK8sClient.Get(ctx, healthRecordName, healthRecord)

			By("setting the finalizer on work on creation")
			Expect(healthRecord.GetFinalizers()).To(ContainElement("kratix.io/health-record-cleanup"))

			By("removing the healthRecord from the status of the associated resource")
			Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
			_, err := t.reconcileUntilCompletion(reconciler, healthRecord)
			Expect(err).NotTo(HaveOccurred())

			record := &v1alpha1.HealthRecord{}
			fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)
			Expect(fakeK8sClient.Get(ctx, healthRecordName, record)).To(MatchError(ContainSubstring("not found")))
			status, ok := updatedResource.Object["status"].(map[string]any)
			Expect(ok).To(BeTrue())
			healthStatus, ok := status["healthStatus"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(healthStatus).To(HaveKeyWithValue("healthRecords", BeNil()))
		})
	})

	When("the resource request has already been deleted", func() {
		var healthRecordName types.NamespacedName

		BeforeEach(func() {
			_ = reconcile()
			Expect(fakeK8sClient.Delete(ctx, resource)).To(Succeed())

			healthRecordName = types.NamespacedName{
				Name:      healthRecord.GetName(),
				Namespace: healthRecord.GetNamespace(),
			}
			Expect(fakeK8sClient.Get(ctx, healthRecordName, healthRecord)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
		})

		It("removes the finalizer since the owner no longer exists", func() {
			_, err := t.reconcileUntilCompletion(reconciler, healthRecord)
			Expect(err).NotTo(HaveOccurred())

			record := &v1alpha1.HealthRecord{}
			err = fakeK8sClient.Get(ctx, healthRecordName, record)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	When("the promise has already been deleted", func() {
		var healthRecordName types.NamespacedName

		BeforeEach(func() {
			_ = reconcile()
			Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())

			healthRecordName = types.NamespacedName{
				Name:      healthRecord.GetName(),
				Namespace: healthRecord.GetNamespace(),
			}
			Expect(fakeK8sClient.Get(ctx, healthRecordName, healthRecord)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
		})

		It("removes the finalizer so the healthRecord can be deleted", func() {
			_, err := t.reconcileUntilCompletion(reconciler, healthRecord)
			Expect(err).NotTo(HaveOccurred())

			record := &v1alpha1.HealthRecord{}
			err = fakeK8sClient.Get(ctx, healthRecordName, record)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	When("two healthRecords for the same resource are deleted together", func() {
		var second *v1alpha1.HealthRecord
		var firstName, secondName types.NamespacedName

		BeforeEach(func() {
			second = &v1alpha1.HealthRecord{
				TypeMeta: metav1.TypeMeta{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "HealthRecord",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "another-name", Namespace: "default"},
				Data: v1alpha1.HealthRecordData{
					PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
					ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
					State:       "ready",
					LastRun:     now,
					Details:     details,
				},
			}
			Expect(fakeK8sClient.Create(ctx, second)).To(Succeed())

			// Both listed on the resource, each carrying the finalizer.
			_, err := t.reconcileUntilCompletion(reconciler, healthRecord)
			Expect(err).NotTo(HaveOccurred())
			_, err = t.reconcileUntilCompletion(reconciler, second)
			Expect(err).NotTo(HaveOccurred())

			firstName = types.NamespacedName{Name: healthRecord.GetName(), Namespace: healthRecord.GetNamespace()}
			secondName = types.NamespacedName{Name: second.GetName(), Namespace: second.GetNamespace()}

			Expect(fakeK8sClient.Get(ctx, firstName, healthRecord)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, secondName, second)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, second)).To(Succeed())
		})

		// Alternating single passes, not one record to completion: completing
		// one tidies up before the other starts, which hides the bug.
		It("removes both finalizers rather than each re-adding the other", func() {
			gone := func(name types.NamespacedName) bool {
				return apierrors.IsNotFound(fakeK8sClient.Get(ctx, name, &v1alpha1.HealthRecord{}))
			}

			Eventually(func(g Gomega) {
				for _, name := range []types.NamespacedName{firstName, secondName} {
					if gone(name) {
						continue
					}
					_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: name})
					g.Expect(err).NotTo(HaveOccurred())
				}
				g.Expect(gone(firstName)).To(BeTrue(), "the first record never lost its finalizer")
				g.Expect(gone(secondName)).To(BeTrue(), "the second record never lost its finalizer")
			}).Should(Succeed())
		})
	})

	When("the promise has already been deleted but the healthRecord is not being deleted", func() {
		var healthRecordName types.NamespacedName

		BeforeEach(func() {
			_ = reconcile()
			Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())

			healthRecordName = types.NamespacedName{
				Name:      healthRecord.GetName(),
				Namespace: healthRecord.GetNamespace(),
			}
		})

		It("leaves the healthRecord and finalizer unchanged", func() {
			result, err := t.reconcileUntilCompletion(reconciler, healthRecord)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			record := &v1alpha1.HealthRecord{}
			Expect(fakeK8sClient.Get(ctx, healthRecordName, record)).To(Succeed())
			Expect(record.GetFinalizers()).To(ContainElement("kratix.io/health-record-cleanup"))
		})
	})
	When("the resource expects health records at a promise version", func() {
		const version = expectedPromiseVersion

		var anotherRecord func(name, namespace, state, promiseVersion string) *v1alpha1.HealthRecord

		condition := func(r *unstructured.Unstructured) *clusterv1.Condition {
			GinkgoHelper()
			cond := resourceutil.GetCondition(r, resourceutil.HealthChecksSucceededCondition)
			Expect(cond).NotTo(BeNil(), "HealthChecksSucceeded condition missing")
			return cond
		}

		setHealthDefinitions := func(count int64) {
			GinkgoHelper()
			Expect(unstructured.SetNestedField(resource.Object, count, "status", "healthStatus", "healthDefinitions")).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())
		}

		BeforeEach(func() {
			anotherRecord = func(name, namespace, state, promiseVersion string) *v1alpha1.HealthRecord {
				return createVersionedRecord(promise, resource, name, namespace, state, promiseVersion, now)
			}

			Expect(unstructured.SetNestedMap(resource.Object, map[string]any{
				"healthStatus": map[string]any{"expectedPromiseVersion": version, "healthDefinitions": int64(1)},
			}, "status")).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())

			healthRecord.Data.State = "healthy"
			healthRecord.Data.PromiseVersion = "v1.0.0"
			Expect(fakeK8sClient.Update(ctx, healthRecord)).To(Succeed())
		})

		When("one HealthDefinition is placed on two destinations", func() {
			var work *v1alpha1.Work

			BeforeEach(func() {
				work = createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				placeWorkGroup(work, 0, "worker-2")
			})

			It("waits for records when only the previous version has reported", func() {
				updated := reconcile()

				healthStatus := getResourceHealthStatus(updated)
				Expect(healthStatus).To(HaveKeyWithValue("expectedRecords", int64(2)))
				Expect(healthStatus).To(HaveKeyWithValue("state", "healthy"))
				records := getHealthRecordsList(getResourceStatus(updated))
				Expect(records).To(HaveLen(1))
				Expect(records[0]).To(HaveKeyWithValue("promiseVersion", "v1.0.0"))

				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksWaitingForRecordsReason))
				Expect(cond.Message).To(Equal("0 of 2 records have reported at v2.0.0"))
			})

			It("waits for records when one of two has reported", func() {
				anotherRecord("b-name", "default", "healthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(2)))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksWaitingForRecordsReason))
				Expect(cond.Message).To(Equal("1 of 2 records have reported at v2.0.0"))
			})

			It("succeeds when every destination has reported healthy", func() {
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "ready", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "healthy"))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksAllRecordsHealthyReason))
				Expect(cond.Message).To(Equal("2 of 2 records have reported at v2.0.0"))
			})

			It("fails when one record is unhealthy", func() {
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "unhealthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "unhealthy"))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionFalse))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksUnhealthyReason))
				Expect(cond.Message).To(Equal("1 of 2 records at v2.0.0 is unhealthy"))
			})

			It("fails when one record is degraded", func() {
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "degraded", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "degraded"))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionFalse))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksDegradedReason))
				Expect(cond.Message).To(Equal("1 of 2 records at v2.0.0 is degraded"))
			})

			It("reports no health checks when the version ships none", func() {
				setHealthDefinitions(0)

				updated := reconcile()

				healthStatus := getResourceHealthStatus(updated)
				Expect(healthStatus).To(HaveKeyWithValue("expectedRecords", int64(0)))
				Expect(getHealthRecordsList(getResourceStatus(updated))).To(HaveLen(1))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksNoHealthChecksReason))
				Expect(cond.Message).To(Equal("v2.0.0 ships no health checks"))
			})

			It("keeps the condition true when an unversioned record is unhealthy", func() {
				healthRecord.Data.State = "unhealthy"
				healthRecord.Data.PromiseVersion = ""
				Expect(fakeK8sClient.Update(ctx, healthRecord)).To(Succeed())
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "healthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "unhealthy"))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksAllRecordsHealthyReason))
				Expect(cond.Message).To(Equal("2 of 2 records have reported at v2.0.0"))
			})

			It("keeps lastTransitionTime while the status stays the same", func() {
				anHourAgo := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
				Expect(unstructured.SetNestedSlice(resource.Object, []any{map[string]any{
					"type":               string(resourceutil.HealthChecksSucceededCondition),
					"status":             string(v1.ConditionTrue),
					"reason":             resourceutil.HealthChecksAllRecordsHealthyReason,
					"message":            "2 of 2 records have reported at v2.0.0",
					"lastTransitionTime": anHourAgo.Format(time.RFC3339),
				}}, "status", "conditions")).To(Succeed())
				Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "healthy", version)

				By("reconciling twice with the same inputs")
				reconcile()
				cond := condition(reconcile())
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Message).To(Equal("2 of 2 records have reported at v2.0.0"))
				Expect(cond.LastTransitionTime.Time).To(BeTemporally("==", anHourAgo))

				By("changing only the message")
				anotherRecord("d-name", "default", "healthy", version)
				cond = condition(reconcile())
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Message).To(Equal("3 of 2 records have reported at v2.0.0"))
				Expect(cond.LastTransitionTime.Time).To(BeTemporally("==", anHourAgo))
			})

			It("counts every record that reported, even beyond the expected count", func() {
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "default", "healthy", version)
				anotherRecord("d-name", "default", "healthy", version)

				cond := condition(reconcile())
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksAllRecordsHealthyReason))
				Expect(cond.Message).To(Equal("3 of 2 records have reported at v2.0.0"))
			})

			It("judges only records in the resource namespace", func() {
				anotherRecord("b-name", "default", "healthy", version)
				anotherRecord("c-name", "other-ns", "healthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "healthy"))
				Expect(getHealthRecordSources(getResourceStatus(updated))).To(ConsistOf(
					"default/a-name", "default/b-name", "other-ns/c-name",
				))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Message).To(Equal("1 of 2 records have reported at v2.0.0"))
			})

			It("returns to waiting when a reported record is deleted", func() {
				anotherRecord("b-name", "default", "healthy", version)
				deleted := anotherRecord("c-name", "default", "healthy", version)
				_, err := t.reconcileUntilCompletion(reconciler, deleted)
				Expect(err).NotTo(HaveOccurred())
				Expect(condition(reconcile()).Status).To(Equal(v1.ConditionTrue))

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(deleted), deleted)).To(Succeed())
				Expect(fakeK8sClient.Delete(ctx, deleted)).To(Succeed())
				_, err = t.reconcileUntilCompletion(reconciler, deleted)
				Expect(err).NotTo(HaveOccurred())

				updated := &unstructured.Unstructured{}
				updated.SetGroupVersionKind(resource.GroupVersionKind())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updated)).To(Succeed())
				Expect(getHealthRecordsList(getResourceStatus(updated))).To(HaveLen(2))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksWaitingForRecordsReason))
				Expect(cond.Message).To(Equal("1 of 2 records have reported at v2.0.0"))
			})

			It("copies the health summary and condition onto the resource binding", func() {
				binding := bindingForResource(promise, resource, version)
				anotherRecord("b-name", "default", "healthy", version)

				updated := reconcile()
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())

				expectedRecords := int64(2)
				Expect(binding.Status.HealthStatus).To(Equal(&v1alpha1.ResourceBindingHealthStatus{
					State:                  "healthy",
					ExpectedPromiseVersion: version,
					ExpectedRecords:        &expectedRecords,
				}))

				resourceCond := condition(updated)
				bindingCond := apimeta.FindStatusCondition(binding.Status.Conditions, string(resourceutil.HealthChecksSucceededCondition))
				Expect(bindingCond).NotTo(BeNil())
				Expect(*bindingCond).To(Equal(metav1.Condition{
					Type:               string(resourceCond.Type),
					Status:             metav1.ConditionStatus(resourceCond.Status),
					Reason:             resourceCond.Reason,
					Message:            resourceCond.Message,
					LastTransitionTime: resourceCond.LastTransitionTime,
				}))
				Expect(bindingCond.Message).To(Equal("1 of 2 records have reported at v2.0.0"))

				statusJSON, err := json.Marshal(binding.Status)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(statusJSON)).NotTo(ContainSubstring("healthRecords"))
			})

			It("drops the condition from the binding once the resource no longer carries it", func() {
				binding := bindingForResource(promise, resource, version)
				apimeta.SetStatusCondition(&binding.Status.Conditions, metav1.Condition{
					Type: v1alpha1.UpgradeSucceededCondition, Status: metav1.ConditionTrue, Reason: "Upgraded",
				})
				Expect(fakeK8sClient.Status().Update(ctx, binding)).To(Succeed())
				healthType := string(resourceutil.HealthChecksSucceededCondition)

				updated := reconcile()
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())
				Expect(apimeta.FindStatusCondition(binding.Status.Conditions, healthType)).NotTo(BeNil())

				removeCondition(updated, resourceutil.HealthChecksSucceededCondition)
				unstructured.RemoveNestedField(updated.Object, "status", "healthStatus", "expectedPromiseVersion")
				Expect(fakeK8sClient.Status().Update(ctx, updated)).To(Succeed())

				reconcile()
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())
				Expect(apimeta.FindStatusCondition(binding.Status.Conditions, healthType)).To(BeNil())
				Expect(apimeta.FindStatusCondition(binding.Status.Conditions, v1alpha1.UpgradeSucceededCondition)).NotTo(BeNil())
			})

			It("requeues without an error when another writer updated the binding first", func() {
				binding := bindingForResource(promise, resource, "latest")
				anotherRecord("b-name", "default", "healthy", version)
				conflicting := &bindingHealthConflictClient{Client: fakeK8sClient, conflicts: 1}
				reconciler.Client = conflicting

				result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(healthRecord)})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(BeNumerically(">", 0))
				Expect(conflicting.conflicts).To(BeZero(), "the binding update never conflicted")

				reconcile()
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())
				Expect(binding.Status.HealthStatus).NotTo(BeNil())
				Expect(binding.Status.HealthStatus.ExpectedRecords).To(HaveValue(BeEquivalentTo(2)))
			})

			It("requeues without an error when the binding update conflicts while a record is deleted", func() {
				binding := bindingForResource(promise, resource, "latest")
				anotherRecord("b-name", "default", "healthy", version)
				healthRecord.Data.State = "unhealthy"
				Expect(fakeK8sClient.Update(ctx, healthRecord)).To(Succeed())
				reconcile()

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(healthRecord), healthRecord)).To(Succeed())
				Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())
				conflicting := &bindingHealthConflictClient{Client: fakeK8sClient, conflicts: 1}
				reconciler.Client = conflicting

				result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(healthRecord)})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(BeNumerically(">", 0))
				Expect(conflicting.conflicts).To(BeZero(), "the binding update never conflicted")

				_, err = t.reconcileUntilCompletion(reconciler, healthRecord)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())
				Expect(binding.Status.HealthStatus.State).To(Equal("healthy"))
			})
		})

		When("no destination has been given the health checks yet", func() {
			It("waits for placement rather than reporting no health checks", func() {
				createWorkForResource(promise, resource, "work-a", 1)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(0)))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksWaitingForRecordsReason))
				Expect(cond.Message).To(Equal("health checks for v2.0.0 have not been placed on a destination yet"))
			})
		})

		When("two HealthDefinitions are placed on one destination", func() {
			It("expects a record per HealthDefinition", func() {
				setHealthDefinitions(2)
				work := createWorkForResource(promise, resource, "work-a", 2)
				placeWorkGroup(work, 0, "worker-1")
				anotherRecord("b-name", "default", "healthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(2)))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionUnknown))
				Expect(cond.Message).To(Equal("1 of 2 records have reported at v2.0.0"))
			})
		})

		When("a second work carries no health check", func() {
			It("expects records only from the work that does", func() {
				withCheck := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(withCheck, 0, "worker-1")
				withoutCheck := createWorkForResource(promise, resource, "work-b", 0)
				placeWorkGroup(withoutCheck, 0, "worker-2")
				anotherRecord("b-name", "default", "healthy", version)

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(1)))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksAllRecordsHealthyReason))
				Expect(cond.Message).To(Equal("1 of 1 records have reported at v2.0.0"))
			})
		})

		When("a work has several workload groups", func() {
			It("counts the HealthDefinitions in each group's content", func() {
				work := createWorkForResource(promise, resource, "work-a", 1, 0)
				placeWorkGroup(work, 0, "worker-1")
				placeWorkGroup(work, 1, "worker-2")

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(1)))
				Expect(condition(updated).Message).To(Equal("0 of 1 records have reported at v2.0.0"))
			})
		})

		When("the promise runs its pipelines in a dedicated namespace", func() {
			BeforeEach(func() {
				promise.Spec.Workflows.Config.PipelineNamespace = "kratix-pipelines"
				Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
			})

			It("counts the works in the pipeline namespace that belong to this resource", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				Expect(work.GetNamespace()).To(Equal("kratix-pipelines"))
				placeWorkGroup(work, 0, "worker-1")
				anotherRecord("b-name", "default", "healthy", version)

				sameName := resource.DeepCopy()
				sameName.SetNamespace("team-b")
				sameName.SetResourceVersion("")
				Expect(fakeK8sClient.Create(ctx, sameName)).To(Succeed())
				other := createWorkForResource(promise, sameName, "work-b", 1)
				placeWorkGroup(other, 0, "worker-1")

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(1)))
				cond := condition(updated)
				Expect(cond.Status).To(Equal(v1.ConditionTrue))
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksAllRecordsHealthyReason))
			})
		})

		When("a work carries a health-definitions annotation that is not a number", func() {
			It("counts nothing for that work and still rolls up the state", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				work.Annotations[v1alpha1.HealthDefinitionsAnnotation] = "many"
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(0)))
				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("state", "healthy"))
				cond := condition(updated)
				Expect(cond.Reason).To(Equal(resourceutil.HealthChecksWaitingForRecordsReason))
				Expect(cond.Message).To(Equal("health checks for v2.0.0 have not been placed on a destination yet"))
			})
		})

		When("one of two works is being deleted", func() {
			It("does not count the deleting work", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				deleting := createWorkForResource(promise, resource, "work-b", 1)
				placeWorkGroup(deleting, 0, "worker-2")
				deleting.Finalizers = []string{"kratix.io/test"}
				Expect(fakeK8sClient.Update(ctx, deleting)).To(Succeed())
				Expect(fakeK8sClient.Delete(ctx, deleting)).To(Succeed())

				updated := reconcile()

				Expect(getResourceHealthStatus(updated)).To(HaveKeyWithValue("expectedRecords", int64(1)))
			})
		})

		When("a work is a dry run", func() {
			It("does not count it", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				work.Labels[v1alpha1.DryRunLabel] = "true"
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())

				Expect(getResourceHealthStatus(reconcile())).To(HaveKeyWithValue("expectedRecords", int64(0)))
			})
		})

		When("a work carries health checks for another promise version", func() {
			It("does not count it", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				work.Annotations[v1alpha1.HealthDefinitionsVersionAnnotation] = "v1.0.0"
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())

				Expect(getResourceHealthStatus(reconcile())).To(HaveKeyWithValue("expectedRecords", int64(0)))
			})
		})

		When("one of two placements is being deleted", func() {
			It("does not count the deleting placement", func() {
				work := createWorkForResource(promise, resource, "work-a", 1)
				placeWorkGroup(work, 0, "worker-1")
				placeWorkGroup(work, 0, "worker-2")
				deleting := &v1alpha1.WorkPlacement{}
				key := types.NamespacedName{Name: "work-a.work-a-group-0.worker-2", Namespace: work.GetNamespace()}
				Expect(fakeK8sClient.Get(ctx, key, deleting)).To(Succeed())
				deleting.Finalizers = []string{"kratix.io/test"}
				Expect(fakeK8sClient.Update(ctx, deleting)).To(Succeed())
				Expect(fakeK8sClient.Delete(ctx, deleting)).To(Succeed())

				Expect(getResourceHealthStatus(reconcile())).To(HaveKeyWithValue("expectedRecords", int64(1)))
			})
		})

		When("one group's workload content cannot be decompressed", func() {
			It("counts nothing for that group and the rest of the work still counts", func() {
				work := createWorkForResource(promise, resource, "work-a", 1, 1)
				work.Spec.WorkloadGroups[1].Workloads[0].Content = "not compressed"
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())
				placeWorkGroup(work, 0, "worker-1")
				placeWorkGroup(work, 1, "worker-2")

				Expect(getResourceHealthStatus(reconcile())).To(HaveKeyWithValue("expectedRecords", int64(1)))
			})
		})

		DescribeTable("the condition message",
			func(healthDefinitions int64, placements int, states []string, reason, message string) {
				setHealthDefinitions(healthDefinitions)
				if healthDefinitions > 0 {
					work := createWorkForResource(promise, resource, "work-a", 1)
					for i := range placements {
						placeWorkGroup(work, 0, fmt.Sprintf("worker-%d", i))
					}
				}
				for i, state := range states {
					anotherRecord(fmt.Sprintf("record-%d", i), "default", state, version)
				}

				cond := condition(reconcile())
				Expect(cond.Reason).To(Equal(reason))
				Expect(cond.Message).To(Equal(message))
			},
			Entry("no health checks", int64(0), 0, nil, "NoHealthChecks", "v2.0.0 ships no health checks"),
			Entry("not placed", int64(1), 0, nil, "WaitingForRecords", "health checks for v2.0.0 have not been placed on a destination yet"),
			Entry("unhealthy before placement", int64(1), 0, []string{"unhealthy"}, "Unhealthy", "1 of 0 records at v2.0.0 is unhealthy"),
			Entry("waiting", int64(1), 2, []string{"healthy"}, "WaitingForRecords", "1 of 2 records have reported at v2.0.0"),
			Entry("unknown is not reported", int64(1), 2, []string{"healthy", "unknown"}, "WaitingForRecords", "1 of 2 records have reported at v2.0.0"),
			Entry("one unhealthy", int64(1), 2, []string{"healthy", "unhealthy"}, "Unhealthy", "1 of 2 records at v2.0.0 is unhealthy"),
			Entry("two unhealthy", int64(1), 2, []string{"unhealthy", "unhealthy"}, "Unhealthy", "2 of 2 records at v2.0.0 are unhealthy"),
			Entry("one degraded", int64(1), 2, []string{"healthy", "degraded"}, "Degraded", "1 of 2 records at v2.0.0 is degraded"),
			Entry("two degraded", int64(1), 2, []string{"degraded", "degraded"}, "Degraded", "2 of 2 records at v2.0.0 are degraded"),
			Entry("all healthy", int64(1), 2, []string{"healthy", "ready"}, "AllRecordsHealthy", "2 of 2 records have reported at v2.0.0"),
		)
	})

	When("the resource does not expect health records at a promise version", func() {
		It("rolls up the state without writing a condition or an expected count", func() {
			work := createWorkForResource(promise, resource, "work-a", 1)
			placeWorkGroup(work, 0, "worker-1")

			updated := reconcile()

			healthStatus := getResourceHealthStatus(updated)
			Expect(healthStatus).To(HaveKeyWithValue("state", "ready"))
			Expect(getHealthRecordsList(getResourceStatus(updated))).To(HaveLen(1))
			Expect(healthStatus).NotTo(HaveKey("expectedRecords"))
			Expect(resourceutil.GetCondition(updated, resourceutil.HealthChecksSucceededCondition)).To(BeNil())
		})

		It("mirrors the state onto the binding without an expected count", func() {
			binding := bindingForResource(promise, resource, expectedPromiseVersion)

			reconcile()

			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(binding), binding)).To(Succeed())
			Expect(binding.Status.HealthStatus).To(Equal(&v1alpha1.ResourceBindingHealthStatus{State: "ready"}))
			Expect(binding.Status.Conditions).To(BeEmpty())
		})
	})

})

func getResourceStatus(r *unstructured.Unstructured) map[string]interface{} {
	status, foundHealthRecord, err := unstructured.NestedMap(r.Object, "status")
	Expect(err).ToNot(HaveOccurred())
	Expect(foundHealthRecord).To(BeTrue())
	return status
}

func getResourceHealthStatus(r *unstructured.Unstructured) map[string]any {
	healthStatus, found, err := unstructured.NestedMap(r.Object, "status", "healthStatus")
	Expect(err).ToNot(HaveOccurred())
	Expect(found).To(BeTrue(), "healthStatus key not found in status")
	return healthStatus
}

func getHealthRecordsList(status map[string]interface{}) (healthRecords []any) {
	healthStatus, found := status["healthStatus"]
	Expect(found).To(BeTrue(), "healthStatus key not found in status")

	status, ok := healthStatus.(map[string]interface{})
	Expect(ok).To(BeTrue())
	records, ok := status["healthRecords"].([]any)
	Expect(ok).To(BeTrue())

	return records
}

func getHealthStatusState(status map[string]interface{}) (state string) {
	healthStatus, found := status["healthStatus"]
	Expect(found).To(BeTrue(), "healthStatus key not found in status")

	status, ok := healthStatus.(map[string]interface{})
	Expect(ok).To(BeTrue())

	stateString, ok := status["state"].(string)

	Expect(ok).To(BeTrue())

	return stateString
}

func getHealthRecordSources(status map[string]interface{}) (sources []string) {
	for _, record := range getHealthRecordsList(status) {
		source, ok := record.(map[string]any)["source"].(map[string]any)
		Expect(ok).To(BeTrue())
		sources = append(sources, fmt.Sprintf("%s/%s", source["namespace"], source["name"]))
	}
	return sources
}

func createVersionedRecord(
	promise *v1alpha1.Promise, resource *unstructured.Unstructured, name, namespace, state, promiseVersion string, lastRun int64,
) *v1alpha1.HealthRecord {
	GinkgoHelper()
	record := &v1alpha1.HealthRecord{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: v1alpha1.HealthRecordData{
			PromiseRef:     v1alpha1.PromiseRef{Name: promise.GetName()},
			ResourceRef:    v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
			State:          state,
			PromiseVersion: promiseVersion,
			LastRun:        lastRun,
		},
	}
	Expect(fakeK8sClient.Create(ctx, record)).To(Succeed())
	return record
}

// createWorkForResource builds a Work with one workload group per entry in
// healthDefinitions, each carrying that many HealthDefinition documents.
func createWorkForResource(
	promise *v1alpha1.Promise, resource *unstructured.Unstructured, name string, healthDefinitions ...int,
) *v1alpha1.Work {
	GinkgoHelper()
	total := 0
	var groups []v1alpha1.WorkloadGroup
	for i, count := range healthDefinitions {
		total += count
		content, err := compression.CompressContent(healthDefinitionsYAML(count))
		Expect(err).NotTo(HaveOccurred())
		groups = append(groups, v1alpha1.WorkloadGroup{
			ID:        fmt.Sprintf("%s-group-%d", name, i),
			Directory: ".",
			Workloads: []v1alpha1.Workload{{Filepath: "health.yaml", Content: string(content)}},
		})
	}
	namespace, resourceNamespace := resource.GetNamespace(), ""
	if promise.WorkflowPipelineNamespaceSet() {
		namespace, resourceNamespace = promise.WorkflowPipelineNamespace(), resource.GetNamespace()
	}
	work := &v1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    resourceutil.GetWorkLabels(promise.GetName(), resource.GetName(), resourceNamespace, "", v1alpha1.WorkTypeResource),
			Annotations: map[string]string{
				v1alpha1.HealthDefinitionsVersionAnnotation: expectedPromiseVersion,
				v1alpha1.HealthDefinitionsAnnotation:        strconv.Itoa(total),
			},
		},
		Spec: v1alpha1.WorkSpec{PromiseName: promise.GetName(), ResourceName: resource.GetName(), WorkloadGroups: groups},
	}
	Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
	return work
}

func healthDefinitionsYAML(count int) []byte {
	var out strings.Builder
	for i := range count {
		fmt.Fprintf(&out, "apiVersion: platform.kratix.io/v1alpha1\nkind: HealthDefinition\nmetadata:\n  name: check-%d\n---\n", i)
	}
	out.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: not-a-check\n")
	return []byte(out.String())
}

func placeWorkGroup(work *v1alpha1.Work, group int, destination string) {
	GinkgoHelper()
	groupID := work.Spec.WorkloadGroups[group].ID
	placement := &v1alpha1.WorkPlacement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%s.%s", work.GetName(), groupID, destination),
			Namespace: work.GetNamespace(),
			Labels: map[string]string{
				"kratix.io/work":                      work.GetName(),
				"kratix.io/workload-group-id":         groupID,
				controller.TargetDestinationNameLabel: destination,
			},
		},
		Spec: v1alpha1.WorkPlacementSpec{
			TargetDestinationName: destination,
			PromiseName:           work.Spec.PromiseName,
			ResourceName:          work.Spec.ResourceName,
			ID:                    groupID,
		},
	}
	Expect(fakeK8sClient.Create(ctx, placement)).To(Succeed())
}

func bindingForResource(promise *v1alpha1.Promise, resource *unstructured.Unstructured, version string) *v1alpha1.ResourceBinding {
	GinkgoHelper()
	binding := &v1alpha1.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-binding",
			Namespace: resource.GetNamespace(),
			Labels: map[string]string{
				v1alpha1.PromiseNameLabel:  promise.GetName(),
				v1alpha1.ResourceNameLabel: resource.GetName(),
			},
		},
		Spec: v1alpha1.ResourceBindingSpec{
			Version:     version,
			PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
			ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
		},
	}
	Expect(fakeK8sClient.Create(ctx, binding)).To(Succeed())
	return binding
}
