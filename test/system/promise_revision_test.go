package system_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Upgrade", func() {
	promiseName := "upgrade"
	rrOneName := "upgrade-rr-one"
	rrTwoName := "upgrade-rr-two"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	AfterEach(func() {
		if getEnvOrDefault("UPGRADE_ENABLED", "false") != "true" {
			return
		}

		platform.EventuallyKubectlDelete("upgrades", rrTwoName)
		platform.EventuallyKubectlDelete("promise", promiseName)
	})

	It("works", func() {
		if getEnvOrDefault("UPGRADE_ENABLED", "false") != "true" {
			Skip("skipping upgrade test suite because UPGRADE_ENABLED is not set to true")
		}

		initialPromiseVersion := "v0.1.0"
		updatedPromiseVersion := "v0.2.0"

		rrOneBeforeUpgradeCMName := "before-upgrade-banana"
		rrOneAfterUpgradeCMName := "after-upgrade-banana"
		rrTwoBeforeUpgradeCMName := "before-upgrade-apple"
		rrTwoAfterUpgradeCMName := "after-upgrade-apple"

		By("creating a promise revision at promise installation", func() {
			platform.Kubectl("apply", "-f", "assets/upgrades/promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(SatisfyAll(
				ContainSubstring("Available"),
				ContainSubstring(initialPromiseVersion)))

			Eventually(func() string {
				return platform.KubectlAllowFail("get", "promiserevisions",
					fmt.Sprintf("%s-%s", promiseName, initialPromiseVersion),
					"-o=jsonpath='{.status.latest}'")
			}).Should(ContainSubstring("true"))
		})

		By("creating two resources", func() {
			platform.Kubectl("apply", "-f", "assets/upgrades/resource-request.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrOneName)
			}).Should(ContainSubstring("Reconciled"))

			platform.Kubectl("apply", "-f", "assets/upgrades/resource-request-2.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName)
			}).Should(ContainSubstring("Reconciled"))

		})

		By("creating a resource binding when making a request", func() {
			for _, resourceName := range []string{rrOneName, rrTwoName} {
				Eventually(func(g Gomega) {
					name := getBindingName(promiseName, resourceName)
					g.Expect(name).To(ContainSubstring(fmt.Sprintf("%s-%s", resourceName, promiseName)))
					g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring("latest"))
				}).Should(Succeed())
			}
		})

		By("pinning resource two to the current version", func() {
			bindingName := getBindingName(promiseName, rrTwoName)

			patch := []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/version",
					"value": "v0.1.0",
				},
			}

			b, marshalErr := json.Marshal(patch)
			Expect(marshalErr).NotTo(HaveOccurred())
			platform.Kubectl("patch", bindingName, "--type=json", "-p", string(b))
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrOneName, "-ojsonpath='{.status.promiseVersion}'")
			}).Should(ContainSubstring(initialPromiseVersion))
		})

		By("verifying the generated config maps", func() {
			Eventually(func() string {
				return worker.Kubectl("get", "configmap")
			}).Should(
				SatisfyAll(
					ContainSubstring(rrOneBeforeUpgradeCMName),
					ContainSubstring(rrTwoBeforeUpgradeCMName),
				),
			)
		})

		By("creating a new promise revision when a new promise version is installed", func() {
			platform.Kubectl("apply", "-f", "assets/upgrades/promise-new-version.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(SatisfyAll(
				ContainSubstring("Available"),
				ContainSubstring(updatedPromiseVersion)))

			Eventually(func() string {
				return platform.Kubectl("get", "promiserevisions")
			}).Should(SatisfyAll(
				ContainSubstring(fmt.Sprintf("%s-%s", promiseName, initialPromiseVersion)),
				ContainSubstring(fmt.Sprintf("%s-%s", promiseName, updatedPromiseVersion))))
		})

		By("updating the resource request pinned to latest", func() {
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrOneName, "-ojsonpath='{.status.promiseVersion}'")
			}, time.Second*30).Should(ContainSubstring(updatedPromiseVersion))

			Eventually(func() string {
				return worker.Kubectl("get", "configmap")
			}).Should(ContainSubstring(rrOneAfterUpgradeCMName))
		})

		By("not updating the resource request pinned to a specific version", func() {
			Consistently(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName, "-ojsonpath='{.status.promiseVersion}'")
			}, time.Second*5).Should(ContainSubstring(initialPromiseVersion))

			Consistently(func() string {
				return worker.Kubectl("get", "configmap")
			}, time.Second*5).Should(ContainSubstring(rrTwoBeforeUpgradeCMName))
		})

		By("updating the v0.1.0 resource binding to v0.2.0", func() {
			bindingName := getBindingName(promiseName, rrTwoName)

			patch := []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/version",
					"value": "v0.2.0",
				},
			}

			b, marshalErr := json.Marshal(patch)
			Expect(marshalErr).NotTo(HaveOccurred())
			platform.Kubectl("patch", bindingName, "--type=json", "-p", string(b))
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName, "-ojsonpath='{.status.promiseVersion}'")
			}).Should(ContainSubstring(updatedPromiseVersion))
		})

		By("upgrading the resource to v0.2.0", func() {
			Eventually(func() string {
				return worker.Kubectl("get", "configmap")
			}).Should(SatisfyAll(
				ContainSubstring(rrTwoAfterUpgradeCMName),
				Not(ContainSubstring(rrTwoBeforeUpgradeCMName))))
		})

		By("restoring the bindings correctly when they are deleted", func() {
			platform.EventuallyKubectlDelete("resourcebindings", "--all")

			Eventually(func(g Gomega) {
				resourceOneBindingName := getBindingName(promiseName, rrOneName)
				resourceTwoBindingName := getBindingName(promiseName, rrTwoName)

				g.Expect(platform.Kubectl("get", "--namespace=default", resourceOneBindingName, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring("latest"))
				g.Expect(platform.Kubectl("get", "--namespace=default", resourceTwoBindingName, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring("v0.2.0"))
			}).Should(Succeed())
		})

		By("cleaning up the right resource binding when a request is deleted", func() {
			platform.EventuallyKubectlDelete("upgrades", rrOneName)

			bindingLabels := strings.Join([]string{
				fmt.Sprintf("kratix.io/promise-name=%s", promiseName),
			}, ",")

			Eventually(func() string {
				return platform.Kubectl("get", "--namespace=default",
					"resourcebindings", "-l", bindingLabels)
			}).Should(SatisfyAll(
				ContainSubstring(fmt.Sprintf("%s-%s", rrTwoName, promiseName)),
				Not(ContainSubstring(rrOneName)),
			))
		})
	})
})

func getBindingName(promiseName, resourceName string) string {
	resourceBindingLabels := strings.Join([]string{
		fmt.Sprintf("kratix.io/resource-name=%s", resourceName),
		fmt.Sprintf("kratix.io/promise-name=%s", promiseName),
	}, ",")
	return strings.TrimSpace(
		platform.Kubectl("get", "--namespace=default", "resourcebindings", "-l", resourceBindingLabels, "-o=name"),
	)
}
