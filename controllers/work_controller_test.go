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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/controllers/controllersfakes"
	"github.com/syntasso/kratix/lib/hash"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var work *platformv1alpha1.Work

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
		work = &platformv1alpha1.Work{}
		work.Name = workResourceName
		work.Namespace = "default"
		work.Spec.Replicas = platformv1alpha1.ResourceRequestReplicas
		work.Spec.WorkloadGroups = []platformv1alpha1.WorkloadGroup{
			{
				ID: hash.ComputeHash("."),
				Workloads: []platformv1alpha1.Workload{
					{
						Content: "{someApi: foo, someValue: bar}",
					},
					{
						Content: "{someApi: baz, someValue: bat}",
					},
				},
			},
		}
	})

	When("the work is being created", func() {
		When("the scheduler is able to schedule the work successfully", func() {
			BeforeEach(func() {
				fakeScheduler.ReconcileWorkReturns([]string{}, nil)
				Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())
			})
			It("re-reconciles until completion", func() {
				_, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).ToNot(HaveOccurred())
			})
		})

		When("the scheduler returns an error reconciling the work", func() {
			var schedulingErrorString = "scheduling error"
			BeforeEach(func() {
				fakeScheduler.ReconcileWorkReturns([]string{}, errors.New(schedulingErrorString))
				Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())

			})

			It("returns an error and requeues", func() {
				_, err := t.reconcileUntilCompletion(reconciler, work)
				Expect(err).To(MatchError(schedulingErrorString))
			})
		})

		When("the scheduler returns work that could not be scheduled", func() {
			When("the work is a resource request", func() {
				BeforeEach(func() {
					workloadGroupIds := []string{"5058f1af8388633f609cadb75a75dc9d"}
					fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
					Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())

				})
				It("re-reconciles unless destinations become available", func() {
					_, err := t.reconcileUntilCompletion(reconciler, work)
					Expect(err).To(MatchError("reconcile loop detected"))
				})
			})

			When("the work is a dependency", func() {
				BeforeEach(func() {
					workloadGroupIds := []string{"5058f1af8388633f609cadb75a75dc9d"}
					work.Spec.Replicas = -1
					fakeScheduler.ReconcileWorkReturns(workloadGroupIds, nil)
					Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, workName, work)).To(Succeed())

				})
				It("re-reconciles unless destinations become available", func() {
					result, err := t.reconcileUntilCompletion(reconciler, work)
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})

		})

		// When the work cannot be found
		// When the work cannot be scheduled
		// When there are no available destinations (surfaced already)
	})

	Describe("On Work updates", func() {
		var work *platformv1alpha1.Work
		var workPlacementGenID int64

		AfterEach(func() {
			deleteWork(work)
		})

		Describe("changes to spec.workloads", func() {
			BeforeEach(func() {
				work = createWork(platformv1alpha1.ResourceRequestReplicas, nil)
				workPlacementList := waitForWorkPlacements(work)
				Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")
				workPlacementGenID = workPlacementList.Items[0].GetGeneration()
			})

			When("the work.spec.workload changes", func() {
				It("updates the workplacement workloads", func() {
					work = getWork(work.GetName(), work.GetNamespace())
					work.Spec.WorkloadGroups[0].Workloads = append(work.Spec.WorkloadGroups[0].Workloads, platformv1alpha1.Workload{
						Content: "{someApi: newApi, someValue: newValue}",
					})
					Expect(k8sClient.Update(context.Background(), work)).To(Succeed())

					Eventually(func(g Gomega) {
						workPlacementList := waitForWorkPlacements(work)
						g.Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")

						workPlacement := workPlacementList.Items[0]
						g.Expect(workPlacement.Spec.Workloads).To(HaveLen(3), "expected three Workloads")

						g.Expect(workPlacement.GetGeneration()).ToNot(Equal(workPlacementGenID))
					})
				})
			})

			When("the work.spec.workload does not change", func() {
				It("updates the workplacement workloads", func() {
					work = getWork(work.GetName(), work.GetNamespace())
					work.Spec.ResourceName = "newResourceName"
					Expect(k8sClient.Update(context.Background(), work)).To(Succeed())

					Consistently(func() bool {
						workPlacementList := workPlacementsFor(work)
						return workPlacementList.Items[0].GetGeneration() == workPlacementGenID
					}).Should(BeTrue())
				})
			})
		})

		When("the work is for Resources", func() {
			var (
				anotherDestination *platformv1alpha1.Destination
			)

			BeforeEach(func() {
				anotherDestination = createDestination("worker-2", map[string]string{"destination": "worker-2"})

				work = createWork(platformv1alpha1.ResourceRequestReplicas, &platformv1alpha1.WorkloadGroupScheduling{
					MatchLabels: map[string]string{"destination": "worker-1"},
					Source:      "promise",
				})
				workPlacementList := waitForWorkPlacements(work)
				Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")
			})

			AfterEach(func() {
				k8sClient.Delete(context.Background(), anotherDestination)
			})

			When("the scheduling changes", func() {
				It("does not create a new workplacement", func() {
					work = getWork(work.GetName(), work.GetNamespace())
					work.Spec.WorkloadGroups[0].DestinationSelectors[0].MatchLabels = map[string]string{"destination": "worker-2"}

					Expect(k8sClient.Update(context.Background(), work)).To(Succeed())
					Consistently(func() int {
						workPlacementList := workPlacementsFor(work)
						return len(workPlacementList.Items)
					}, consistentlyTimeout, interval).Should(Equal(1))
				})
			})
		})

		When("the work is for dependencies", func() {
			When("the work.spec.destinationSelectors.promise changes", func() {
				var (
					anotherDestination *platformv1alpha1.Destination
				)

				BeforeEach(func() {
					anotherDestination = createDestination("worker-2", map[string]string{"destination": "worker-2"})
					//worker-1 and worker-2
					work = createWork(platformv1alpha1.DependencyReplicas, &platformv1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{"destination": "worker-1"},
						Source:      "promise",
					},
					)

					workPlacements := waitForWorkPlacements(work)
					Expect(workPlacements.Items).To(HaveLen(1))

					work = getWork(work.Name, work.Namespace)
					work.Spec.WorkloadGroups[0].DestinationSelectors[0].MatchLabels = map[string]string{"destination": "worker-2"}
					Expect(k8sClient.Update(context.Background(), work)).To(Succeed())
				})

				It("creates any missing workplacements", func() {
					Eventually(func() bool {
						workPlacements := workPlacementsFor(work)

						for _, workPlacement := range workPlacements.Items {
							if strings.HasPrefix(workPlacement.Name, work.Name+".worker-2") {
								return true
							}
						}

						return false
					}, "5s").Should(BeTrue(), "WorkPlacement for worker-2 was never created")
				})

				It("does not remove old workplacements", func() {
					Eventually(func() bool {
						workPlacements := workPlacementsFor(work)
						for _, workPlacement := range workPlacements.Items {
							if strings.HasPrefix(workPlacement.Name, work.Name+".worker-1") {
								return true
							}
						}
						return false
					}, "5s").Should(BeTrue(), "WorkPlacement for worker-1 was deleted")
				})

				AfterEach(func() {
					k8sClient.Delete(context.Background(), anotherDestination)
				})
			})
		})
	})
})

func waitForWorkPlacements(work *platformv1alpha1.Work) *platformv1alpha1.WorkPlacementList {
	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	Eventually(func() int {
		workPlacementList = workPlacementsFor(work)
		return len(workPlacementList.Items)
	}, timeout, interval).Should(BeNumerically(">", 0))
	return workPlacementList
}

func workPlacementsFor(work *platformv1alpha1.Work) *platformv1alpha1.WorkPlacementList {
	allWorkPlacements := &platformv1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace: work.Namespace,
	}
	err := k8sClient.List(context.Background(), allWorkPlacements, workPlacementListOptions)
	Expect(err).ToNot(HaveOccurred())

	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	for _, workPlacement := range allWorkPlacements.Items {
		if strings.HasPrefix(workPlacement.Name, work.Name) {
			workPlacementList.Items = append(workPlacementList.Items, workPlacement)
		}
	}

	return workPlacementList
}

func createWork(replicas int, destinationSelectors *platformv1alpha1.WorkloadGroupScheduling) *platformv1alpha1.Work {
	work = &platformv1alpha1.Work{}
	work.Name = "work-" + rand.String(10)
	work.Spec.ResourceName = "someName"
	work.Namespace = "default"
	work.Spec.Replicas = replicas
	work.Spec.WorkloadGroups = []platformv1alpha1.WorkloadGroup{
		{
			Directory: ".",
			ID:        hash.ComputeHash("."),
			Workloads: []platformv1alpha1.Workload{
				{
					Content: "{someApi: foo, someValue: bar}",
				},
				{
					Content: "{someApi: baz, someValue: bat}",
				},
			},
		},
	}
	if destinationSelectors != nil {
		work.Spec.WorkloadGroups[0].DestinationSelectors = []platformv1alpha1.WorkloadGroupScheduling{
			*destinationSelectors,
		}
	}
	err := k8sClient.Create(context.Background(), work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

func deleteWork(work *platformv1alpha1.Work) {
	if work == nil {
		return
	}
	err := k8sClient.Delete(context.Background(), work)
	Expect(err).ToNot(HaveOccurred())
}

func getWork(name, namespace string) *platformv1alpha1.Work {
	work := &platformv1alpha1.Work{}
	err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: namespace}, work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

func createDestination(name string, labels ...map[string]string) *v1alpha1.Destination {
	destination := &v1alpha1.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1alpha1.KratixSystemNamespace,
		},
	}

	if len(labels) > 0 {
		destination.SetLabels(labels[0])
	}

	Expect(k8sClient.Create(context.Background(), destination)).To(Succeed())
	return destination
}
