/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/controller/controllerfakes"
	"github.com/syntasso/kratix/lib/hash"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("WorkReconciler", func() {
	var (
		ctx               context.Context
		reconciler        *controller.WorkReconciler
		fakeScheduler     *controllerfakes.FakeWorkScheduler
		fakeEventRecorder *events.FakeRecorder
		workName          types.NamespacedName
		work              *v1alpha1.Work
		workResourceName  = "work-controller-test-resource-request"
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeScheduler = &controllerfakes.FakeWorkScheduler{}
		fakeScheduler.ReconcileWorkReturns([]string{}, nil)
		fakeEventRecorder = events.NewFakeRecorder(1024)
		reconciler = &controller.WorkReconciler{
			Client:        fakeK8sClient,
			Log:           ctrl.Log.WithName("controllers").WithName("Work"),
			Scheduler:     fakeScheduler,
			EventRecorder: fakeEventRecorder,
		}

		workName = types.NamespacedName{
			Name:      workResourceName,
			Namespace: "default",
		}
		work = &v1alpha1.Work{
			TypeMeta: v1.TypeMeta{
				Kind:       "work",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      workResourceName,
				Namespace: "default",
			},
			Spec: v1alpha1.WorkSpec{
				ResourceName: "resource-name",
				WorkloadGroups: []v1alpha1.WorkloadGroup{
					{
						ID: hash.ComputeHash("."),
						Workloads: []v1alpha1.Workload{
							{
								Content: "{someApi: foo, someValue: bar}",
							},
							{
								Content: "{someApi: baz, someValue: bat}",
							},
						},
					},
					{
						ID: hash.ComputeHash("somedir"),
						Workloads: []v1alpha1.Workload{
							{
								Content: "{someApi: foo, someValue: bar}",
							},
							{
								Content: "{someApi: baz, someValue: bat}",
							},
						},
					},
				},
			},
		}
		Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
	})

	When("the resource does not exist", func() {
		It("succeeds and does not requeue", func() {
			work.Name = "non-existent-work"
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the scheduler is able to schedule the work successfully", func() {
		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

	})

	When("scheduled workplacements failed to write", func() {
		BeforeEach(func() {
			markWorkAsScheduled(work)

			wp := v1alpha1.WorkPlacement{
				ObjectMeta: v1.ObjectMeta{
					Name:      "test",
					Namespace: work.Namespace,
					Labels: map[string]string{
						"kratix.io/work": work.Name,
					},
				},
				Status: v1alpha1.WorkPlacementStatus{
					Conditions: []v1.Condition{
						{
							Type:   "WriteSucceeded",
							Status: v1.ConditionFalse,
						},
					},
				},
			}
			Expect(fakeK8sClient.Create(context.TODO(), &wp)).To(Succeed())
			Expect(fakeK8sClient.Status().Update(context.TODO(), &wp)).To(Succeed())
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		It("sets the right status condition", func() {
			Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(work), work)).To(Succeed())
			Expect(work.Status.Conditions).To(HaveLen(3))
			readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(readyCond).ToNot(BeNil())
			Expect(readyCond.Status).To(Equal(v1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("WorkplacementsFailing"))
			Expect(readyCond.Message).To(Equal("Failing"))
			Expect(readyCond.ObservedGeneration).To(Equal(work.GetGeneration()))
		})

		It("leaves ScheduleSucceeded alone, as scheduling did succeed", func() {
			Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(work), work)).To(Succeed())
			scheduleSucceededCond := apimeta.FindStatusCondition(work.Status.Conditions, "ScheduleSucceeded")
			Expect(scheduleSucceededCond).NotTo(BeNil())
			Expect(scheduleSucceededCond.Status).To(Equal(v1.ConditionTrue))
			Expect(scheduleSucceededCond.Reason).To(Equal("AllWorkplacementsScheduled"))
		})

		It("sends the right events", func() {
			Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(work), work)).To(Succeed())
			Expect(work.Status.Conditions).To(HaveLen(3))
			Eventually(fakeEventRecorder.Events).Should(
				Receive(ContainSubstring("Workplacements failed to write: [test]")))
		})

		It("does not send the event again when nothing has changed", func() {
			Eventually(fakeEventRecorder.Events).Should(
				Receive(ContainSubstring("Workplacements failed to write: [test]")))

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Consistently(fakeEventRecorder.Events).ShouldNot(
				Receive(ContainSubstring("Workplacements failed to write: [test]")))
		})
	})

	When("every workload group has been scheduled", func() {
		BeforeEach(func() {
			markWorkAsScheduled(work)
		})

		It("marks the Work as Ready once every WorkPlacement has written the current generation", func() {
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())
			createWrittenWorkPlacement(work, "second-group", work.Spec.WorkloadGroups[1].ID, work.GetGeneration())

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(readyCond.Status).To(Equal(v1.ConditionTrue))
			Expect(readyCond.Reason).To(Equal("AllWorkplacementsWritten"))
			Expect(readyCond.ObservedGeneration).To(Equal(work.GetGeneration()))
		})

		It("keeps the Work Pending while a WorkPlacement holds an older generation", func() {
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())
			createWrittenWorkPlacement(work, "second-group", work.Spec.WorkloadGroups[1].ID, work.GetGeneration()-1)

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(readyCond.Status).To(Equal(v1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("WaitingForWorkplacementsToBeWritten"))
			Expect(readyCond.Message).To(Equal("Pending"))
		})

		It("keeps the Work Pending while a workload group has no WorkPlacement", func() {
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			Expect(work.IsReadyAtCurrentGeneration()).To(BeFalse())
		})

		It("marks the Work as Ready when it has no workload groups", func() {
			work.Spec.WorkloadGroups = nil
			Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			Expect(work.IsReadyAtCurrentGeneration()).To(BeTrue())
		})
	})

	When("some workload groups could not be scheduled", func() {
		BeforeEach(func() {
			work.Spec.ResourceName = ""
			Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())

			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type:    "ScheduleSucceeded",
				Status:  v1.ConditionFalse,
				Reason:  "UnscheduledWorkloads",
				Message: "No matching destination found",
			})
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type:    "Ready",
				Status:  v1.ConditionFalse,
				Reason:  "UnscheduledWorkloads",
				Message: "Pending",
			})
			Expect(fakeK8sClient.Status().Update(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
		})

		It("leaves the Ready condition the scheduler set", func() {
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(v1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("UnscheduledWorkloads"))
		})
	})

	When("a misplaced Work's Ready still says Failing from an earlier write error", func() {
		BeforeEach(func() {
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "ScheduleSucceeded", Status: v1.ConditionFalse, Reason: "DestinationSelectorMismatch",
				Message: "Target destination no longer matches destinationSelectors for workloadGroups: [x]",
			})
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "Ready", Status: v1.ConditionFalse, Reason: "WorkplacementsFailing", Message: "Failing",
			})
			Expect(fakeK8sClient.Status().Update(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
		})

		It("sets Ready back to Misplaced once no WorkPlacement is failing", func() {
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: u")
			})

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			ready := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(ready.Status).To(Equal(v1.ConditionFalse))
			Expect(ready.Reason).To(Equal("Misplaced"))
			Expect(ready.Message).To(Equal("Misplaced"))
		})

		It("keeps Ready Failing while a WorkPlacement is still failing", func() {
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteFailedCondition(errors.New("authentication required"))
			})

			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			ready := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(ready.Reason).To(Equal("WorkplacementsFailing"))
			Expect(ready.Message).To(Equal("Failing"))
		})
	})

	When("a resource Work has an unscheduled workload group and a scheduled one", func() {
		reconcileOnce := func() ctrl.Result {
			GinkgoHelper()
			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: workName})
			Expect(err).NotTo(HaveOccurred())
			return result
		}

		BeforeEach(func() {
			fakeScheduler.ReconcileWorkReturns([]string{work.Spec.WorkloadGroups[0].ID}, nil)
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "ScheduleSucceeded", Status: v1.ConditionFalse, Reason: "UnscheduledWorkloads", Message: "No matching destination found",
			})
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "Ready", Status: v1.ConditionFalse, Reason: "UnscheduledWorkloads", Message: "Pending",
			})
			Expect(fakeK8sClient.Status().Update(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			createWorkPlacementWithStatus(work, "b", "dev", work.Spec.WorkloadGroups[1].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: u")
			})
			reconcileOnce()
			reconcileOnce()
		})

		It("records the write state while waiting for a Destination", func() {
			placement := &v1alpha1.WorkPlacement{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: work.Name + "-b", Namespace: work.Namespace}, placement)).To(Succeed())
			placement.SetWriteSucceededCondition()
			Expect(fakeK8sClient.Status().Update(ctx, placement)).To(Succeed())

			Expect(reconcileOnce()).To(Equal(ctrl.Result{RequeueAfter: 60 * time.Second}))

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			Expect(work.Status.WorkPlacementStatuses).To(Equal([]v1alpha1.WorkPlacementWriteStatus{{
				Name:        work.Name + "-b",
				Destination: "dev",
				Status:      "True",
				Reason:      "WorkloadsWrittenToStateStore",
				Message:     "Workloads written to State Store",
			}}))
			write := apimeta.FindStatusCondition(work.Status.Conditions, v1alpha1.WriteSucceededConditionType)
			Expect(write).NotTo(BeNil())
			Expect(write.Status).To(Equal(v1.ConditionUnknown))
			Expect(write.Reason).To(Equal("WaitingForWorkplacementsToBeWritten"))
			ready := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(ready.Reason).To(Equal("UnscheduledWorkloads"))
			Expect(ready.Message).To(Equal("Pending"))
		})
	})

	When("the scheduler returns an error reconciling the work", func() {
		var schedulingErrorString = "scheduling error"

		BeforeEach(func() {
			fakeScheduler.ReconcileWorkReturns([]string{}, errors.New(schedulingErrorString))
		})

		It("errors", func() {
			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).To(MatchError(schedulingErrorString))
		})
	})

	When("the scheduler returns work that could not be scheduled", func() {
		When("the work is a resource request", func() {
			var workloadGroupIds []string

			BeforeEach(func() {
				workloadGroupIds = []string{"5058f1af8388633f609cadb75a75dc9d"}
				work.Spec.ResourceName = "resource-name"
				fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			})

			It("re-reconciles until a destinations become available", func() {
				_, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).To(MatchError("reconcile loop detected"))

				fakeScheduler.ReconcileWorkReturns(nil, nil)
				result, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			It("send an event", func() {
				_, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).To(MatchError("reconcile loop detected"))

				Eventually(fakeEventRecorder.Events).Should(
					Receive(ContainSubstring("waiting for destination for workload group: [%s]",
						strings.Join(workloadGroupIds, ","))))
			})
		})

		When("the work is a dependency", func() {
			BeforeEach(func() {
				workloadGroupIds := []string{"5058f1af8388633f609cadb75a75dc9d"}
				work.Spec.ResourceName = ""
				fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
				Expect(fakeK8sClient.Update(ctx, work)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())

			})

			It("does not requeue", func() {
				result, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})
	})

	When("work is deleted", func() {
		It("succeeds", func() {
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			By("setting the finalizer on work on creation")
			work := &v1alpha1.Work{}
			Expect(fakeK8sClient.Get(ctx, workName, work)).
				To(Succeed())
			Expect(work.GetFinalizers()).To(ContainElement("kratix.io/work-cleanup"))

			By("cleaning up workplacement with matching label on deletion")
			workPlacement := v1alpha1.WorkPlacement{
				ObjectMeta: v1.ObjectMeta{
					Name:      work.Name,
					Namespace: "default",
					Labels:    map[string]string{v1alpha1.KratixPrefix + "work": work.Name},
				},
				Spec: v1alpha1.WorkPlacementSpec{TargetDestinationName: "test-destination"},
			}
			Expect(fakeK8sClient.Create(ctx, &workPlacement)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: workPlacement.Name, Namespace: workPlacement.Namespace},
				&v1alpha1.WorkPlacement{})).To(Succeed())

			Expect(fakeK8sClient.Delete(ctx, work)).To(Succeed())
			_, err = t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(MatchError(ContainSubstring("not found")))
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: workPlacement.Name, Namespace: workPlacement.Namespace},
				&v1alpha1.WorkPlacement{})).To(MatchError(ContainSubstring("not found")))
		})
	})

	Describe("WorkPlacement write state on the Work", func() {
		writeSucceeded := func() *v1.Condition {
			GinkgoHelper()
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			condition := apimeta.FindStatusCondition(work.Status.Conditions, v1alpha1.WriteSucceededConditionType)
			Expect(condition).NotTo(BeNil())
			return condition
		}

		reconcile := func() {
			GinkgoHelper()
			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
		}

		BeforeEach(func() {
			markWorkAsScheduled(work)
		})

		When("a WorkPlacement is waiting for its delivery", func() {
			BeforeEach(func() {
				createWorkPlacementWithStatus(work, "b", "dev", work.Spec.WorkloadGroups[1].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteSucceededCondition()
				})
				createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteDeferredCondition("Waiting for approval: u")
					wp.Status.DeliveryURL = "https://github.com/org/repo/pull/7"
				})
				reconcile()
			})

			It("lists every WorkPlacement with its write state", func() {
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
				Expect(work.Status.WorkPlacementStatuses).To(Equal([]v1alpha1.WorkPlacementWriteStatus{
					{
						Name:        work.Name + "-a",
						Destination: "prod",
						Status:      "Unknown",
						Reason:      v1alpha1.WorkloadsAwaitingDeliveryReason,
						Message:     "Waiting for approval: u",
						DeliveryURL: "https://github.com/org/repo/pull/7",
					},
					{
						Name:        work.Name + "-b",
						Destination: "dev",
						Status:      "True",
						Reason:      "WorkloadsWrittenToStateStore",
						Message:     "Workloads written to State Store",
					},
				}))
			})

			It("sets WriteSucceeded to Unknown with the Destination and the delivery text", func() {
				condition := writeSucceeded()
				Expect(condition.Status).To(Equal(v1.ConditionUnknown))
				Expect(condition.Reason).To(Equal("WorkplacementsAwaitingDelivery"))
				Expect(condition.Message).To(Equal("prod: Waiting for approval: u"))
				Expect(condition.ObservedGeneration).To(Equal(work.GetGeneration()))
			})

			It("leaves Ready as it was", func() {
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
				ready := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
				Expect(ready.Reason).To(Equal("WaitingForWorkplacementsToBeWritten"))
				Expect(ready.Message).To(Equal("Pending"))
			})
		})

		It("keeps the list in name order", func() {
			reorderListResults = func(list client.ObjectList) {
				if placements, ok := list.(*v1alpha1.WorkPlacementList); ok {
					slices.Reverse(placements.Items)
				}
			}
			for _, name := range []string{"c", "b", "a"} {
				createWorkPlacementWithStatus(work, name, "dev", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteSucceededCondition()
				})
			}
			reconcile()

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			var names []string
			for _, placement := range work.Status.WorkPlacementStatuses {
				names = append(names, placement.Name)
			}
			Expect(names).To(Equal([]string{work.Name + "-a", work.Name + "-b", work.Name + "-c"}))
		})

		It("does not list a WorkPlacement that is being deleted", func() {
			createWorkPlacementWithStatus(work, "a", "dev", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: u")
			})
			deleting := &v1alpha1.WorkPlacement{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: work.Name + "-a", Namespace: work.Namespace}, deleting)).To(Succeed())
			deleting.Finalizers = []string{"kratix.io/test"}
			Expect(fakeK8sClient.Update(ctx, deleting)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, deleting)).To(Succeed())
			reconcile()

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			Expect(work.Status.WorkPlacementStatuses).To(BeEmpty())
			Expect(writeSucceeded().Reason).NotTo(Equal("WorkplacementsAwaitingDelivery"))
		})

		When("one WorkPlacement was rejected and another failed to write", func() {
			BeforeEach(func() {
				createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteRejectedCondition("Rejected in https://github.com/org/repo/pull/7 with no comment")
				})
				createWorkPlacementWithStatus(work, "b", "dev", work.Spec.WorkloadGroups[1].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteFailedCondition(errors.New("authentication required"))
				})
				reconcile()
			})

			It("reports the write failure, not the rejection", func() {
				condition := writeSucceeded()
				Expect(condition.Status).To(Equal(v1.ConditionFalse))
				Expect(condition.Reason).To(Equal("WorkplacementsFailing"))
				Expect(condition.Message).To(Equal("dev: authentication required"))
			})

			It("still marks the Work as Failing", func() {
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
				Expect(apimeta.FindStatusCondition(work.Status.Conditions, "Ready").Message).To(Equal("Failing"))
				Expect(work.Status.WorkPlacementStatuses).To(HaveLen(2))
			})
		})

		It("reports a rejection", func() {
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteRejectedCondition("Rejected in https://github.com/org/repo/pull/7 with no comment")
			})
			createWorkPlacementWithStatus(work, "b", "dev", work.Spec.WorkloadGroups[1].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: u")
			})
			reconcile()

			condition := writeSucceeded()
			Expect(condition.Status).To(Equal(v1.ConditionFalse))
			Expect(condition.Reason).To(Equal("WorkplacementsRejected"))
			Expect(condition.Message).To(Equal("prod: Rejected in https://github.com/org/repo/pull/7 with no comment"))
		})

		It("cuts the joined text to 4096 characters", func() {
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: a")
			})
			for _, name := range []string{"b", "c", "d", "e"} {
				createWorkPlacementWithStatus(work, name, "dev", work.Spec.WorkloadGroups[1].ID, func(wp *v1alpha1.WorkPlacement) {
					wp.SetWriteDeferredCondition("Waiting for approval: " + strings.Repeat("é", 1000))
				})
			}
			reconcile()

			message := writeSucceeded().Message
			Expect(message).To(HavePrefix("prod: Waiting for approval: a; dev: Waiting for approval: é"))
			Expect(utf8.ValidString(message)).To(BeTrue())
			Expect(utf8.RuneCountInString(message)).To(Equal(4096))
			Expect(message).To(HaveSuffix("…"))
		})

		It("cuts each listed message to 1024 characters", func() {
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteFailedCondition(errors.New(strings.Repeat("é", 5000)))
			})
			reconcile()

			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			Expect(work.Status.WorkPlacementStatuses).To(HaveLen(1))
			message := work.Status.WorkPlacementStatuses[0].Message
			Expect(utf8.ValidString(message)).To(BeTrue())
			Expect(utf8.RuneCountInString(message)).To(Equal(1024))
			Expect(message).To(Equal(strings.Repeat("é", 1023) + "…"))
		})

		It("sets WriteSucceeded to True once all are written", func() {
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())
			createWrittenWorkPlacement(work, "second-group", work.Spec.WorkloadGroups[1].ID, work.GetGeneration())
			reconcile()

			condition := writeSucceeded()
			Expect(condition.Status).To(Equal(v1.ConditionTrue))
			Expect(condition.Reason).To(Equal("WorkplacementsWritten"))
			Expect(condition.Message).To(Equal("All workloads written"))
		})

		It("keeps WriteSucceeded Unknown while a WorkPlacement is behind", func() {
			createWrittenWorkPlacement(work, "first-group", work.Spec.WorkloadGroups[0].ID, work.GetGeneration())
			createWrittenWorkPlacement(work, "second-group", work.Spec.WorkloadGroups[1].ID, work.GetGeneration()-1)
			reconcile()

			condition := writeSucceeded()
			Expect(condition.Status).To(Equal(v1.ConditionUnknown))
			Expect(condition.Reason).To(Equal("WaitingForWorkplacementsToBeWritten"))
		})

		It("records the write state while the scheduler owns Ready", func() {
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "ScheduleSucceeded", Status: v1.ConditionFalse, Reason: "UnscheduledWorkloads", Message: "No matching destination found",
			})
			apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
				Type: "Ready", Status: v1.ConditionFalse, Reason: "Misplaced", Message: "Misplaced",
			})
			Expect(fakeK8sClient.Status().Update(ctx, work)).To(Succeed())
			createWorkPlacementWithStatus(work, "a", "prod", work.Spec.WorkloadGroups[0].ID, func(wp *v1alpha1.WorkPlacement) {
				wp.SetWriteDeferredCondition("Waiting for approval: u")
			})
			reconcile()

			Expect(writeSucceeded().Reason).To(Equal("WorkplacementsAwaitingDelivery"))
			Expect(work.Status.WorkPlacementStatuses).To(HaveLen(1))
			Expect(apimeta.FindStatusCondition(work.Status.Conditions, "Ready").Message).To(Equal("Misplaced"))
		})
	})

	When("a WorkPlacement writes a new version of the Work", func() {
		It("reconciles the Work again", func() {
			oldWorkPlacement := &v1alpha1.WorkPlacement{ObjectMeta: v1.ObjectMeta{Generation: 1}}
			oldWorkPlacement.SetWriteSucceededCondition()
			newWorkPlacement := oldWorkPlacement.DeepCopy()
			newWorkPlacement.Generation = 2
			newWorkPlacement.SetWriteSucceededCondition()

			shouldReconcile := controller.WorkPlacementStatusUpdatePredicate().Update(event.UpdateEvent{
				ObjectOld: oldWorkPlacement,
				ObjectNew: newWorkPlacement,
			})

			Expect(shouldReconcile).To(BeTrue())
		})
	})

	Describe("which WorkPlacement changes reconcile the Work", func() {
		var oldWorkPlacement *v1alpha1.WorkPlacement

		shouldReconcile := func(newWorkPlacement *v1alpha1.WorkPlacement) bool {
			return controller.WorkPlacementStatusUpdatePredicate().Update(event.UpdateEvent{
				ObjectOld: oldWorkPlacement,
				ObjectNew: newWorkPlacement,
			})
		}

		BeforeEach(func() {
			oldWorkPlacement = &v1alpha1.WorkPlacement{ObjectMeta: v1.ObjectMeta{Generation: 1}}
			oldWorkPlacement.SetWriteDeferredCondition("Waiting for approval: https://github.com/org/repo/pull/7")
			oldWorkPlacement.Status.DeliveryURL = "https://github.com/org/repo/pull/7"
		})

		It("reconciles when only the write message changes", func() {
			newWorkPlacement := oldWorkPlacement.DeepCopy()
			newWorkPlacement.SetWriteDeferredCondition("Waiting for approval: https://github.com/org/repo/pull/8")

			Expect(shouldReconcile(newWorkPlacement)).To(BeTrue())
		})

		It("reconciles when only the delivery URL changes", func() {
			newWorkPlacement := oldWorkPlacement.DeepCopy()
			newWorkPlacement.Status.DeliveryURL = "https://github.com/org/repo/pull/8"

			Expect(shouldReconcile(newWorkPlacement)).To(BeTrue())
		})

		It("does not reconcile when nothing it reads changes", func() {
			newWorkPlacement := oldWorkPlacement.DeepCopy()
			newWorkPlacement.Status.VersionID = "abc123"

			Expect(shouldReconcile(newWorkPlacement)).To(BeFalse())
		})
	})
})

func markWorkAsScheduled(work *v1alpha1.Work) {
	GinkgoHelper()
	apimeta.SetStatusCondition(&work.Status.Conditions, v1.Condition{
		Type:    "ScheduleSucceeded",
		Status:  v1.ConditionTrue,
		Reason:  "AllWorkplacementsScheduled",
		Message: "All workplacements scheduled successfully",
	})
	Expect(fakeK8sClient.Status().Update(context.TODO(), work)).To(Succeed())
	Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(work), work)).To(Succeed())
}

func createWrittenWorkPlacement(work *v1alpha1.Work, name, workloadGroupID string, workGeneration int64) {
	GinkgoHelper()
	workPlacement := &v1alpha1.WorkPlacement{
		ObjectMeta: v1.ObjectMeta{
			Name:      work.Name + "-" + name,
			Namespace: work.Namespace,
			Labels:    map[string]string{"kratix.io/work": work.Name},
		},
		Spec: v1alpha1.WorkPlacementSpec{
			ID:             workloadGroupID,
			WorkGeneration: workGeneration,
		},
	}
	Expect(fakeK8sClient.Create(context.TODO(), workPlacement)).To(Succeed())
	Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(workPlacement), workPlacement)).To(Succeed())
	workPlacement.SetWriteSucceededCondition()
	Expect(fakeK8sClient.Status().Update(context.TODO(), workPlacement)).To(Succeed())
}

func createWorkPlacementWithStatus(work *v1alpha1.Work, name, destination, workloadGroupID string, setStatus func(*v1alpha1.WorkPlacement)) {
	GinkgoHelper()
	workPlacement := &v1alpha1.WorkPlacement{
		ObjectMeta: v1.ObjectMeta{
			Name:      work.Name + "-" + name,
			Namespace: work.Namespace,
			Labels:    map[string]string{"kratix.io/work": work.Name},
		},
		Spec: v1alpha1.WorkPlacementSpec{
			ID:                    workloadGroupID,
			TargetDestinationName: destination,
			WorkGeneration:        work.GetGeneration(),
		},
	}
	Expect(fakeK8sClient.Create(context.TODO(), workPlacement)).To(Succeed())
	Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(workPlacement), workPlacement)).To(Succeed())
	setStatus(workPlacement)
	Expect(fakeK8sClient.Status().Update(context.TODO(), workPlacement)).To(Succeed())
}
