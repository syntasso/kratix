package newsystem_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/newsystem/kubeutils"
)

const timeout, interval = 2 * time.Minute, 2 * time.Second

var _ = Describe("System Tests", func() {
	var platform *kubeutils.Cluster
	var worker1 *kubeutils.Cluster
	var worker2 *kubeutils.Cluster

	BeforeAll(func() {
		kubeutils.SetTimeoutAndInterval(timeout, interval)

		platform = &kubeutils.Cluster{Context: "kind-platform", Name: "platform-cluster"}
		worker1 = &kubeutils.Cluster{Context: "kind-worker", Name: "worker-cluster-1"}
		worker2 = &kubeutils.Cluster{Context: "kind-worker-2", Name: "worker-cluster-2"}
	})

	Describe("Kratix Core Functionality", func() {
		It("should deliver xaas to users", func() {
			By("successfully installing a promise", func() {
				Expect(platform.Kubectl("apply", "-f", "assets/promise.yaml")).To(ContainSubstring("test-promise created"))

				By("applying the promise api as a crd", func() {
					Eventually(func() {
						platform.Kubectl("get", "crd")
					}, timeout, interval).Should(ContainSubstring("testbundles.test.kratix.io"))
				})

				By("running the promise configure pipeline", func() {
					Eventually(func() {
						platform.Kubectl("get", "pods", "-n", "kratix-platform-system")
					}).Should(ContainSubstring("setup-deps"))
				})

				By("deploying the dependencies to the correct destinations", func() {
					Eventually(func(g Gomega) {
						g.Expect(worker1.Kubectl("get", "namespaces")).To(ContainSubstring("testbundle-ns"))
						g.Expect(worker2.Kubectl("get", "namespaces")).To(ContainSubstring("testbundle-ns"))

						g.Expect(worker1.Kubectl("get", "configmap")).To(ContainSubstring("testbundle-cm"))
						g.Expect(worker2.Kubectl("get", "configmap")).To(ContainSubstring("testbundle-cm"))
					}, timeout, interval).Should(Succeed())
				})
			})
		})
	})
})

/*

Later Stage
- health record test --> later stage
- promise release --> later stage
- filepathMode set to none --> later stage
- requiredPromises --> later stage
	the promise has requirements that are fulfilled, it can fulfil resource requests once requirements are met
- rbac testing
	creating multiple resource requests in different namespaces, it creates separate bindings for request namespaces and schedules works to correct destinations
- imperative promise testing??
- kratix config file

Promise lifecycle
	installing a promise
		runs the promise pipelines
		sets some status on the Promise?

	updating a promise
		it propagates the changes and re-runs all the pipelines

	deleting a promise
		runs the delete pipeline
		deletes all resources
		deletes all the works

Resource Lifecycle
	request a resource
		it executes the pipelines
		schedules the work to the appropriate destinations
		sets some statuses on the resource

	Update a resource
		it executes the pipelines
		schedules the work to the appropriate destinations
		sets some statuses on the resource

	Delete a resource
		deletes the works and workplacements
		deletes the resources from the destination


Scheduling
	it schedules resources to the correct Destinations
	it allows updates to schedule


1. Chain of containers in a workflow
	reader -> promise w1 -> promise w2 -> work-creator -> status update
2. Garbage collection
    cleaning up the child resources
3. Files written to the state store
4. External connections
   for example: promise releases can pull from url, states stores access s3/git
5. Status
	changing of status as stages goes
6. multiple workflows
	the right one starts after the previous one finishes
7. The things that workflows can output that Kratix interprets (status.yaml and destination-selectors for now)


Promise:

spec:
	api: ✅
	dependencies: ❌
	requiredPromises: ❌
	destinationSelectors: ❌
	workflows:
		resource: [configure] ✅
		promise: [configure] ✅

this promise will have
* resource workflow that uses the api somehow
* dynamic scheduling
* dynamic status
* multiple workflows
	* pipeline 1 sets status
	* pipeline 2 waits for the status

destinations:
- 2 destinations

state store:
- 1: git



*/
