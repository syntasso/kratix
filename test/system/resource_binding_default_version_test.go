package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

const (
	rbVersionAssetsPath     = "assets/resource-binding-default-version"
	rbVersionPromiseName    = "rbversion"
	rbVersionPromiseKind    = "rbversions"
	rbVersionRequestName    = "example"
	rbVersionPromiseVersion = "v1.0.0"
)

// This spec relies on the suite-default Kratix config, so it can run in
// parallel with the rest of the suite. It shares the promise with the Serial
// pinned-config spec below, but Serial specs only start after all parallel
// specs have finished, so they never overlap.
var _ = Describe("ResourceBinding Default Version", func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)

		platform.EventuallyKubectlDelete("promise", rbVersionPromiseName)
		platform.Kubectl("apply", "-f", filepath.Join(rbVersionAssetsPath, "promise.yaml"))
		Eventually(func() string {
			return platform.Kubectl("get", "promise", rbVersionPromiseName)
		}).Should(ContainSubstring("Available"))
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete(rbVersionPromiseKind, rbVersionRequestName)
		platform.EventuallyKubectlDelete("promise", rbVersionPromiseName)
	})

	When("defaultVersion is not set", func() {
		It("creates a resource binding with spec.version set to 'latest'", func() {
			platform.Kubectl("apply", "-f", filepath.Join(rbVersionAssetsPath, "resource-request.yaml"))

			Eventually(func(g Gomega) {
				name := getBindingName(rbVersionPromiseName, rbVersionRequestName)
				g.Expect(name).NotTo(BeEmpty())
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).
					To(ContainSubstring("latest"))
			}).Should(Succeed())
		})
	})
})

var _ = Describe("ResourceBinding Default Version pinned", Label("config-mutating"), Serial, func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)

		platform.EventuallyKubectlDelete("promise", rbVersionPromiseName)
		platform.Kubectl("apply", "-f", filepath.Join(rbVersionAssetsPath, "promise.yaml"))
		Eventually(func() string {
			return platform.Kubectl("get", "promise", rbVersionPromiseName)
		}).Should(ContainSubstring("Available"))
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete(rbVersionPromiseKind, rbVersionRequestName)
		platform.EventuallyKubectlDelete("promise", rbVersionPromiseName)

		platform.Kubectl("apply", "-f", kratixConfigPath)
		restartController()
	})

	When("defaultVersion is set to pinned", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(rbVersionAssetsPath, "kratix-config-pinned.yaml"))
			restartController()
		})

		It("creates a resource binding with spec.version set to the current promise revision version", func() {
			platform.Kubectl("apply", "-f", filepath.Join(rbVersionAssetsPath, "resource-request.yaml"))

			Eventually(func(g Gomega) {
				name := getBindingName(rbVersionPromiseName, rbVersionRequestName)
				g.Expect(name).NotTo(BeEmpty())
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).
					To(ContainSubstring(rbVersionPromiseVersion))
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
