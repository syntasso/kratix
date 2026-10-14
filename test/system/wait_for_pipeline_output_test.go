package system_test

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

const (
	waitForOutputPromiseName = "waitforoutput"
	waitForOutputSecretName  = "wait-for-output-credentials"
)

var _ = Describe("Waiting for a pipeline's output to be written", Ordered, func() {
	BeforeEach(func() {
		if os.Getenv("LRE") == "true" {
			Skip("the state store credentials are for the in-KinD Gitea; not applicable on the LRE")
		}
		SetDefaultEventuallyTimeout(3 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(3*time.Minute, 2*time.Second)

		By("pointing a state store at Gitea with the wrong credentials")
		platform.Kubectl("delete", "secret", waitForOutputSecretName, "-n", "default", "--ignore-not-found")
		platform.Kubectl("create", "secret", "generic", waitForOutputSecretName, "-n", "default",
			"--from-literal=username=invalid",
			"--from-literal=password="+giteaPassword(),
		)
		platform.Kubectl("apply", "-f", "assets/wait-for-output/state-store.yaml")
		platform.Kubectl("apply", "-f", "assets/wait-for-output/destination.yaml")
		platform.Kubectl("apply", "-f", "assets/wait-for-output/promise.yaml")

		Eventually(func() string {
			return platform.Kubectl("get", "crds")
		}).Should(ContainSubstring("waitforoutputs.test.kratix.io"))
	})

	AfterEach(func() {
		if os.Getenv("LRE") == "true" {
			return
		}
		fixWaitForOutputCredentials()
		platform.Kubectl("delete", "promise", waitForOutputPromiseName, "--ignore-not-found")
		platform.Kubectl("delete", "-f", "assets/wait-for-output/destination.yaml", "--ignore-not-found")
		platform.Kubectl("delete", "-f", "assets/wait-for-output/state-store.yaml", "--ignore-not-found")
		platform.Kubectl("delete", "secret", waitForOutputSecretName, "-n", "default", "--ignore-not-found")
	})

	It("waits while the first pipeline's output cannot be written, and carries on once it can", func() {
		platform.Kubectl("apply", "-f", "assets/wait-for-output/resource-with-output.yaml")

		By("not starting the second pipeline while delivery fails", func() {
			Eventually(func() string {
				return platform.Kubectl("get", "waitforoutput", "with-output", phaseJSONPath("first"))
			}).Should(Equal("Succeeded"))

			Eventually(func(g Gomega) {
				g.Expect(platform.Kubectl("get", "waitforoutput", "with-output", messageJSONPath("second"))).To(
					ContainSubstring(`Waiting for the output of pipeline "first" to be written to its Destinations`))
				g.Expect(platform.Kubectl("get", "waitforoutput", "with-output", configureWorkflowCompletedReasonJSONPath)).To(
					Equal("WaitingForPipelineOutput"))
			}).Should(Succeed())

			Consistently(func(g Gomega) {
				g.Expect(platform.Kubectl("get", "waitforoutput", "with-output", phaseJSONPath("second"))).To(Equal("Pending"))
				g.Expect(jobCountForResourcePipeline(waitForOutputPromiseName, "second")).To(Equal(0))
			}, 30*time.Second, 5*time.Second).Should(Succeed())
		})

		By("starting the second pipeline once the credentials are fixed", func() {
			fixWaitForOutputCredentials()

			Eventually(func(g Gomega) {
				g.Expect(platform.Kubectl("get", "waitforoutput", "with-output", phaseJSONPath("second"))).To(Equal("Succeeded"))
				g.Expect(platform.Kubectl("get", "waitforoutput", "with-output", messageJSONPath("second"))).To(BeEmpty())
			}).Should(Succeed())
		})
	})

	It("starts the second pipeline straight away when the first pipeline has no output", func() {
		platform.Kubectl("apply", "-f", "assets/wait-for-output/resource-without-output.yaml")

		Eventually(func(g Gomega) {
			g.Expect(platform.Kubectl("get", "waitforoutput", "without-output", phaseJSONPath("first"))).To(Equal("Succeeded"))
			g.Expect(platform.Kubectl("get", "waitforoutput", "without-output", phaseJSONPath("second"))).To(Equal("Succeeded"))
		}).Should(Succeed())
	})
})

const configureWorkflowCompletedReasonJSONPath = `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].reason}`

func fixWaitForOutputCredentials() {
	GinkgoHelper()
	platform.Kubectl("patch", "secret", waitForOutputSecretName, "-n", "default", "--type=merge",
		"-p", `{"stringData":{"username":"gitea_admin"}}`)
}
