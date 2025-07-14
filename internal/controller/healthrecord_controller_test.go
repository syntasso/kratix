package controller_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("HealthRecordController", func() {
	var (
		now           int64
		healthRecord  *v1alpha1.HealthRecord
		promise       *v1alpha1.Promise
		resource      *unstructured.Unstructured
		details       *runtime.RawExtension
		reconciler    *controller.HealthRecordReconciler
		eventRecorder *record.FakeRecorder
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
		resource = createResourceRequest(resourceRequestPath)

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
				State:       "healthy",
				LastRun:     now,
				Details:     details,
			},
		}

		eventRecorder = record.NewFakeRecorder(1024)

		reconciler = &controller.HealthRecordReconciler{
			Client:        fakeK8sClient,
			Scheme:        scheme.Scheme,
			Log:           GinkgoLogr,
			EventRecorder: eventRecorder,
		}

		Expect(fakeK8sClient.Create(ctx, healthRecord)).To(Succeed())
	})

	When("reconciling against a resource request", func() {
		var updatedResource *unstructured.Unstructured

		BeforeEach(func() {
			updatedResource = reconcile()
		})

		It("updates the resource status.healthRecord with the HealthRecord data", func() {
			status := getResourceStatus(updatedResource)

			records := getHealthRecordsList(status)
			Expect(records[0]).To(HaveKeyWithValue("lastRun", healthRecord.Data.LastRun))
			Expect(records[0]).To(HaveKeyWithValue("state", healthRecord.Data.State))
			Expect(records[0]).To(HaveKeyWithValue("details", HaveKeyWithValue("info", "message")))
			Expect(records[0]).To(HaveKeyWithValue("source", HaveKeyWithValue("name", healthRecord.GetName())))
			Expect(records[0]).To(HaveKeyWithValue("source", HaveKeyWithValue("namespace", healthRecord.GetNamespace())))
		})

		DescribeTable("firing events detailing the healthRecord state",
			func(state string, eventMessage string) {
				Expect(fakeK8sClient.Delete(ctx, healthRecord)).To(Succeed())

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

	When("the resource request already has a HealthRecord with a matching state", func() {
		BeforeEach(func() {
			now = time.Now().Unix()
			healthRecord.Data.State = "ready"
			healthRecord.Data.LastRun = now
			Expect(fakeK8sClient.Update(ctx, healthRecord)).To(Succeed())
		})

		It("updates the run time of the existing HealthRecord", func() {
			updatedResource := reconcile()
			status := getResourceStatus(updatedResource)

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

func getResourceStatus(r *unstructured.Unstructured) map[string]interface{} {
	status, foundHealthRecord, err := unstructured.NestedMap(r.Object, "status")
	Expect(err).ToNot(HaveOccurred())
	Expect(foundHealthRecord).To(BeTrue())
	return status
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
