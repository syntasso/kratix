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

package controllers_test

import (
	"context"
	"errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/controllers/controllersfakes"
	"github.com/syntasso/kratix/lib/hash"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	//+kubebuilder:scaffold:imports
)

var work *v1alpha1.Work

var _ = Describe("WorkReconciler", func() {
	var (
		ctx              context.Context
		reconciler       *controllers.WorkReconciler
		fakeScheduler    *controllersfakes.FakeWorkScheduler
		workName         types.NamespacedName
		work             *v1alpha1.Work
		workResourceName = "work-controller-test-resource-request"
	)

	BeforeEach(func() {
		ctx = context.Background()
		disabled := false
		fakeScheduler = &controllersfakes.FakeWorkScheduler{}
		reconciler = &controllers.WorkReconciler{
			Client:    fakeK8sClient,
			Log:       ctrl.Log.WithName("controllers").WithName("Work"),
			Scheduler: fakeScheduler,
			Disabled:  disabled,
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
				},
			},
		}
	})

	When("the resource does not exist", func() {
		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the scheduler is able to schedule the work successfully", func() {
		BeforeEach(func() {
			fakeScheduler.ReconcileWorkReturns([]string{}, nil)
			Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
		})

		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the scheduler returns an error reconciling the work", func() {
		var schedulingErrorString = "scheduling error"

		BeforeEach(func() {
			fakeScheduler.ReconcileWorkReturns([]string{}, errors.New(schedulingErrorString))
			Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
		})

		It("errors", func() {
			_, err := t.reconcileUntilCompletion(reconciler, work)
			Expect(err).To(MatchError(schedulingErrorString))
		})
	})

	When("the scheduler returns work that could not be scheduled", func() {
		When("the work is a resource request", func() {
			BeforeEach(func() {
				workloadGroupIds := []string{"5058f1af8388633f609cadb75a75dc9d"}
				work.Spec.ResourceName = "resource-name"
				fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
				Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
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
		})

		When("the work is a dependency", func() {
			BeforeEach(func() {
				workloadGroupIds := []string{"5058f1af8388633f609cadb75a75dc9d"}
				work.Spec.ResourceName = ""
				fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
				Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
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
		BeforeEach(func() {
			fakeScheduler.ReconcileWorkReturns([]string{}, nil)
			Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
		})

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
