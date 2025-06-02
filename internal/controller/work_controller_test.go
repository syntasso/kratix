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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/controller/controllerfakes"
	"github.com/syntasso/kratix/lib/hash"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("WorkReconciler", func() {
	var (
		ctx               context.Context
		reconciler        *controller.WorkReconciler
		fakeScheduler     *controllerfakes.FakeWorkScheduler
		fakeEventRecorder *record.FakeRecorder
		workName          types.NamespacedName
		work              *v1alpha1.Work
		workResourceName  = "work-controller-test-resource-request"
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeScheduler = &controllerfakes.FakeWorkScheduler{}
		fakeScheduler.ReconcileWorkReturns([]string{}, nil)
		fakeEventRecorder = record.NewFakeRecorder(1024)
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
			Expect(work.Status.Conditions).To(HaveLen(2))
			readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
			Expect(readyCond).ToNot(BeNil())
			Expect(readyCond.Status).To(Equal(v1.ConditionFalse))
			Expect(readyCond.Reason).To(Equal("WorkplacementsFailing"))
			Expect(readyCond.Message).To(Equal("Failing"))

			scheduleSucceededCond := apimeta.FindStatusCondition(work.Status.Conditions, "ScheduleSucceeded")
			Expect(scheduleSucceededCond).ToNot(BeNil())
			Expect(scheduleSucceededCond.Status).To(Equal(v1.ConditionFalse))
			Expect(scheduleSucceededCond.Reason).To(Equal("WorkplacementsFailing"))
			Expect(scheduleSucceededCond.Message).To(ContainSubstring("Workplacements failed to write: [test]"))
		})

		It("sends the right events", func() {
			Expect(fakeK8sClient.Get(context.TODO(), client.ObjectKeyFromObject(work), work)).To(Succeed())
			Expect(work.Status.Conditions).To(HaveLen(2))
			Eventually(fakeEventRecorder.Events).Should(
				Receive(ContainSubstring("Workplacements failed to write: [test]")))
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
})
