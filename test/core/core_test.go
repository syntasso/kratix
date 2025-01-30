package core_test

import (
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	timeout, interval = 2 * time.Minute, 2 * time.Second
)

var _ = Describe("Core Tests", Ordered, func() {
	var platform *kubeutils.Cluster
	var worker *kubeutils.Cluster

	BeforeAll(func() {
		SetDefaultEventuallyTimeout(timeout)
		SetDefaultEventuallyPollingInterval(interval)
		kubeutils.SetTimeoutAndInterval(timeout, interval)

		platform = &kubeutils.Cluster{Context: "kind-platform", Name: "platform-cluster"}
		worker = &kubeutils.Cluster{Context: "kind-worker", Name: "worker-1"}
		Expect(platform.Kubectl("apply", "-f", "assets/destination.yaml")).To(ContainSubstring("created"))
		platform.Kubectl("label", "destination", "worker-1", "target=worker-1")
		platform.Kubectl("label", "destination", "worker-2", "target=worker-2")
		worker.Kubectl("apply", "-f", "assets/flux.yaml")
	})

	Context("Kratix Core Functionality", func() {
		var (
			resourceRequestName = "kratix-test"
			rrConfigMapName     = "ksystest-cm"      // needs to match assets/example-resource.yaml
			rrNsName            = "ksystest"         // needs to match assets/example-resource.yaml
			rrNsNameUpdated     = "ksystest-updated" // needs to match assets/example-resource-v2.yaml
		)
		It("should deliver xaas to users", func() {
			var originalPromiseConfigMapTimestamp1, originalPromiseConfigMapTimestamp2 string
			By("successfully installing a Promise", func() {
				Expect(platform.Kubectl("apply", "-f", "assets/promise.yaml")).To(ContainSubstring("testbundle created"))

				By("applying the promise api as a crd", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "crd")
					}, timeout, interval).Should(ContainSubstring("testbundles.test.kratix.io"))
				})

				By("running the promise configure pipeline", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "pods", "-n", "kratix-platform-system")
					}).Should(ContainSubstring("setup-deps"))
				})

				By("deploying the dependencies to the correct destinations", func() {
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring("testbundle-ns"))
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring("testbundle-ns"))

						g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "configmap")).To(ContainSubstring("testbundle-cm"))
						g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "configmap")).To(ContainSubstring("testbundle-cm"))
					}, timeout, interval).Should(Succeed())

					originalPromiseConfigMapTimestamp1 = worker.Kubectl("-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.timestamp}")
					originalPromiseConfigMapTimestamp2 = worker.Kubectl("-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.timestamp}")
				})
			})

			By("fulfilling a resource request", func() {
				By("accepting a request", func() {
					Expect(platform.Kubectl("apply", "-f", "assets/example-resource.yaml")).To(ContainSubstring("kratix-test created"))
				})

				By("updating resource status", func() {
					rrArgs := []string{"-n", "default", "get", "testbundle", resourceRequestName}
					generation := platform.Kubectl(append(rrArgs, `-o=jsonpath='{.metadata.generation}'`)...)
					Eventually(func() string {
						return platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.stage}'")...)
					}).Should(ContainSubstring("one"))
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl(append(rrArgs, `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}'`)...)).To(ContainSubstring("True"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.stage}'")...)).To(ContainSubstring("two"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.completed}'")...)).To(ContainSubstring("true"))
						g.Expect(platform.Kubectl(append(rrArgs, `-o=jsonpath='{.status.observedGeneration}'`)...)).To(Equal(generation))
					}).Should(Succeed())

					Eventually(func() bool {
						transitionTime := platform.Kubectl(append(rrArgs, `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].lastTransitionTime}`)...)
						lastSuccessful := platform.Kubectl(append(rrArgs, `-o=jsonpath={.status.lastSuccessfulConfigureWorkflowTime}`)...)
						return transitionTime == lastSuccessful
					}).Should(BeTrue(), "lastTransitionTime should be equal to lastSuccessfulConfigureWorkflowTime")
				})

				cmArgs := []string{"-n", "testbundle-ns", "get", "cm"}
				By("scheduling works to the right destination", func() {
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsName))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-1")...)).To(ContainSubstring(rrConfigMapName))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-2")...)).To(ContainSubstring(rrConfigMapName))
					}).Should(Succeed())
				})

				By("rerunning pipelines when updating a resource request", func() {
					originalTimeStampW1 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)
					originalTimeStampW2 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)
					Expect(platform.Kubectl("apply", "-f", "assets/example-resource-v2.yaml")).To(ContainSubstring("kratix-test configured"))

					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsNameUpdated))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW1))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW2))
					}).Should(Succeed())
				})
			})

			By("updating Promise", func() {
				cmArgs := []string{"-n", "testbundle-ns", "get", "cm"}
				originalTimeStampW1 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)
				originalTimeStampW2 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)

				Expect(platform.Kubectl("apply", "-f", "assets/promise-v2.yaml")).To(ContainSubstring("testbundle configured"))

				By("updating the promise api", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "crd", "testbundles.test.kratix.io", "-oyaml")
					}).Should(ContainSubstring("newName"))
				})

				By("rerunning the promise configure pipeline", func() {
					getCMTimestamp := []string{"-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.timestamp}"}
					getCMEnvValue := []string{"-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.promiseEnv}"}
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl(getCMTimestamp...)).ToNot(Equal(originalPromiseConfigMapTimestamp1))
						g.Expect(worker.Kubectl(getCMTimestamp...)).ToNot(Equal(originalPromiseConfigMapTimestamp2))

						g.Expect(worker.Kubectl(getCMEnvValue...)).To(ContainSubstring("second"))
						g.Expect(worker.Kubectl(getCMEnvValue...)).To(ContainSubstring("second"))
					}).Should(Succeed())
				})

				By("rerunning the resource configure pipeline", func() {
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsNameUpdated))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW1))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW2))
					}).Should(Succeed())
				})
			})

			By("cleaning up after deleting a resource request", func() {
				platform.Kubectl("delete", "-f", "assets/example-resource-v2.yaml")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("-n", "default", "get", "workplacements")).NotTo(ContainSubstring(resourceRequestName))
					g.Expect(platform.Kubectl("-n", "default", "get", "works")).NotTo(ContainSubstring(resourceRequestName))
					g.Expect(worker.Kubectl("get", "namespaces")).NotTo(ContainSubstring(rrNsNameUpdated))
					g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "cm")).NotTo(ContainSubstring(rrConfigMapName))
					g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "cm")).NotTo(ContainSubstring(rrConfigMapName))
				}).Should(Succeed())
			})

			By("cleaning up after uninstalling the Promise", func() {
				platform.Kubectl("delete", "-f", "assets/promise-v2.yaml")

				Eventually(func() string {
					return platform.Kubectl("get", "crd")
				}, timeout, interval).ShouldNot(ContainSubstring("testbundles.test.kratix.io"))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("-n", "kratix-platform-system", "get", "workplacements")).NotTo(ContainSubstring("testbundle"))
					g.Expect(platform.Kubectl("-n", "kratix-platform-system", "get", "works")).NotTo(ContainSubstring("testbundle"))
					g.Expect(worker.Kubectl("get", "namespaces")).NotTo(ContainSubstring("testbundle-ns"))
					g.Expect(worker.Kubectl("get", "namespaces")).NotTo(ContainSubstring("testbundle-ns"))
				}).Should(Succeed())
			})
		})
	})
})
