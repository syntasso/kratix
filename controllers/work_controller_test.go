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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/syntasso/kratix/controllers"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var destination *platformv1alpha1.Destination
var work *platformv1alpha1.Work

var _ = Context("WorkReconciler.Reconcile()", func() {

	var workReconciler *WorkReconciler

	setupControllersOnce := func() {
		if workReconciler == nil {
			scheduler := &Scheduler{
				Client: k8sClient,
				Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
			}
			workReconciler = &WorkReconciler{
				Client:    k8sClient,
				Scheduler: scheduler,
				Log:       ctrl.Log.WithName("controllers").WithName("WorkReconciler"),
			}
			err := workReconciler.SetupWithManager(k8sManager)
			Expect(err).ToNot(HaveOccurred())
		}
	}

	BeforeEach(func() {
		setupControllersOnce()
		destination = &platformv1alpha1.Destination{}
		destination.Name = "worker-1"
		destination.Namespace = "default"
		destination.Labels = map[string]string{"destination": "worker-1"}
		Expect(k8sClient.Create(context.Background(), destination)).To(Succeed())
	})

	AfterEach(func() {
		err := k8sClient.Delete(context.Background(), destination)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("On Work Creation", func() {
		It("creates a WorkPlacement", func() {
			var timeout = "30s"
			var interval = "3s"

			work = &platformv1alpha1.Work{}
			work.Name = "work-controller-test-resource-request"
			work.Namespace = "default"
			work.Spec.Replicas = platformv1alpha1.ResourceRequestReplicas
			err := k8sClient.Create(context.Background(), work)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				workPlacementList := workPlacementsFor(work)
				g.Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")

				var createdWork platformv1alpha1.Work
				err = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(work), &createdWork)
				g.Expect(err).ToNot(HaveOccurred())

				workPlacement := workPlacementList.Items[0]
				g.Expect(workPlacement.GetOwnerReferences()[0].UID).To(Equal(createdWork.GetUID()))

			}, timeout, interval).Should(Succeed(), "WorkPlacement was not created")
		})
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
					work.Spec.Workloads = append(work.Spec.Workloads, platformv1alpha1.WorkloadGroup{
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

				work = createWork(platformv1alpha1.ResourceRequestReplicas, &platformv1alpha1.WorkScheduling{
					Promise: []platformv1alpha1.Selector{
						{MatchLabels: map[string]string{"destination": "worker-1"}},
					},
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
					work.Spec.DestinationSelectors.Promise = []platformv1alpha1.Selector{
						{
							MatchLabels: map[string]string{"destination": "worker-2"},
						},
					}

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
					work = createWork(platformv1alpha1.DependencyReplicas, &platformv1alpha1.WorkScheduling{
						Promise: []platformv1alpha1.Selector{
							{MatchLabels: map[string]string{"destination": "worker-1"}},
						},
					})

					workPlacements := waitForWorkPlacements(work)
					Expect(workPlacements.Items).To(HaveLen(1))

					work = getWork(work.Name, work.Namespace)
					work.Spec.DestinationSelectors.Promise = []platformv1alpha1.Selector{
						{MatchLabels: map[string]string{"destination": "worker-2"}},
					}
					Expect(k8sClient.Update(context.Background(), work)).To(Succeed())
				})

				It("creates any missing workplacements", func() {
					Eventually(func() bool {
						workPlacements := workPlacementsFor(work)

						for _, workPlacement := range workPlacements.Items {
							if workPlacement.Name == work.Name+".worker-2" {
								return true
							}
						}

						return false
					}).Should(BeTrue(), "WorkPlacement for worker-2 was never created")
				})

				It("does not remove old workplacements", func() {
					Eventually(func() bool {
						workPlacements := workPlacementsFor(work)
						for _, workPlacement := range workPlacements.Items {
							if workPlacement.Name == work.Name+".worker-1" {
								return true
							}
						}
						return false
					}).Should(BeTrue(), "WorkPlacement for worker-1 was deleted")
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

func createWork(replicas int, destinationSelectors *platformv1alpha1.WorkScheduling) *platformv1alpha1.Work {
	work = &platformv1alpha1.Work{}
	work.Name = "work-" + rand.String(10)
	work.Spec.ResourceName = "someName"
	work.Namespace = "default"
	work.Spec.Replicas = replicas
	work.Spec.Workloads = []platformv1alpha1.WorkloadGroup{
		{
			Content: "{someApi: foo, someValue: bar}",
		},
		{
			Content: "{someApi: baz, someValue: bat}",
		},
	}
	if destinationSelectors != nil {
		work.Spec.DestinationSelectors = *destinationSelectors
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
