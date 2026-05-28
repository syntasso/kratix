package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("ResourceBinding Default Version", func() {
	const (
		assetsPath     = "assets/resource-binding-default-version"
		promiseName    = "rbversion"
		promiseKind    = "rbversions"
		rrName         = "example"
		promiseVersion = "v1.0.0"
	)

	BeforeEach(func() {
		if getEnvOrDefault("UPGRADE_ENABLED", "false") != "true" {
			Skip("skipping resource binding default version tests because UPGRADE_ENABLED is not set to true")
		}

		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise.yaml"))
		Eventually(func() string {
			return platform.Kubectl("get", "promise", promiseName)
		}).Should(ContainSubstring("Available"))
	})

	AfterEach(func() {
		if getEnvOrDefault("UPGRADE_ENABLED", "false") != "true" {
			return
		}

		platform.EventuallyKubectlDelete(promiseKind, rrName)
		platform.EventuallyKubectlDelete("promise", promiseName)

		platform.Kubectl("apply", "-f", kratixConfigPath)
		restartController()
	})

	When("defaultVersion is not set", func() {
		It("creates a resource binding with spec.version set to 'latest'", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request.yaml"))

			Eventually(func(g Gomega) {
				name := getBindingName(promiseName, rrName)
				g.Expect(name).NotTo(BeEmpty())
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).
					To(ContainSubstring("latest"))
			}).Should(Succeed())
		})
	})

	When("defaultVersion is set to pinned", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-pinned.yaml"))
			restartController()
		})

		It("creates a resource binding with spec.version set to the current promise revision version", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request.yaml"))

			Eventually(func(g Gomega) {
				name := getBindingName(promiseName, rrName)
				g.Expect(name).NotTo(BeEmpty())
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).
					To(ContainSubstring(promiseVersion))
			}).Should(Succeed())
		})
	})
})

func restartController() {
	GinkgoHelper()
	platform.Kubectl("delete", "pod", "-l", "control-plane=controller-manager", "-n", "kratix-platform-system")
	platform.Kubectl("wait", "-n", "kratix-platform-system", "deployments", "-l", "control-plane=controller-manager", "--for=condition=Available")
	Eventually(func() string {
		return platform.KubectlAllowFail("apply", "--dry-run=server", "-f", "assets/kratix-config/promise.yaml")
	}).Should(ContainSubstring("dry run"))
}
