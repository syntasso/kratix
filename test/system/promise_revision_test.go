package system_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Promise Revisions", func() {

	const assetsPath = "assets/promise-revision"

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

	It("is used to manage the lifecycle of resources", func() {
		if getEnvOrDefault("UPGRADE_ENABLED", "false") != "true" {
			Skip("skipping upgrade test suite because UPGRADE_ENABLED is not set to true")
		}

		initialPromiseVersion := "v0.1.0-BETA"
		updatedPromiseVersion := "v0.2.0-NEXTVERSION"

		rrOneBeforeUpgradeCMName := "before-upgrade-banana"
		rrOneAfterUpgradeCMName := "after-upgrade-banana"
		rrTwoBeforeUpgradeCMName := "before-upgrade-apple"
		rrTwoAfterUpgradeCMName := "after-upgrade-apple"

		By("creating a promise revision on promise installation time", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(SatisfyAll(
				ContainSubstring("Available"),
				ContainSubstring(initialPromiseVersion)))

			Eventually(func() string {
				return platform.KubectlAllowFail("get", "promiserevisions",
					"-l", fmt.Sprintf("kratix.io/promise-name=%s,kratix.io/latest-revision=true", promiseName),
				)
			}).Should(SatisfyAll(
				ContainSubstring("NAME"), ContainSubstring(promiseName),
				ContainSubstring("PROMISE"), ContainSubstring(promiseName),
				ContainSubstring("VERSION"), ContainSubstring(initialPromiseVersion),
				ContainSubstring("LATEST"), ContainSubstring("true"),
			))
		})

		By("creating two resources", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrOneName)
			}).Should(ContainSubstring("Reconciled"))

			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request-2.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName)
			}).Should(ContainSubstring("Reconciled"))
		})

		By("creating a resource binding when making a request", func() {
			for _, resourceName := range []string{rrOneName, rrTwoName} {
				Eventually(func(g Gomega) {
					name := getBindingName(promiseName, resourceName)
					g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring("latest"))
					g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.status.lastAppliedVersion}'")).To(ContainSubstring(initialPromiseVersion))
				}).Should(Succeed())
			}
		})

		By("propagating resource request labels to the resource binding", func() {
			Eventually(func(g Gomega) {
				g.Expect(getBindingLabels(promiseName, rrOneName)).To(Equal(map[string]string{
					"environment":             "staging",
					"kratix.io/promise-name":  promiseName,
					"kratix.io/resource-name": rrOneName,
				}))
				// rrTwo has no custom labels, so only the binding-specific labels should be present
				g.Expect(getBindingLabels(promiseName, rrTwoName)).To(Equal(map[string]string{
					"kratix.io/promise-name":  promiseName,
					"kratix.io/resource-name": rrTwoName,
				}))
			}).Should(Succeed())
		})

		By("updating a label on the resource request updates the resource binding", func() {
			platform.Kubectl("label", "--overwrite", "upgrades", rrOneName, "environment=production")
			Eventually(func(g Gomega) {
				g.Expect(getBindingLabels(promiseName, rrOneName)).To(Equal(map[string]string{
					"environment":             "production",
					"kratix.io/promise-name":  promiseName,
					"kratix.io/resource-name": rrOneName,
				}))
			}).Should(Succeed())
		})

		By("removing a label from the resource request removes it from the resource binding", func() {
			platform.Kubectl("label", "upgrades", rrOneName, "environment-")
			Eventually(func(g Gomega) {
				g.Expect(getBindingLabels(promiseName, rrOneName)).To(Equal(map[string]string{
					"kratix.io/promise-name":  promiseName,
					"kratix.io/resource-name": rrOneName,
				}))
			}).Should(Succeed())
		})

		By("pinning resource two to the current version", func() {
			bindingName := getBindingName(promiseName, rrTwoName)

			patch := []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/version",
					"value": initialPromiseVersion,
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
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise-new-version.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(SatisfyAll(
				ContainSubstring("Available"),
				ContainSubstring(updatedPromiseVersion)))
		})

		By("moving the latest revision label to the new promise version", func() {
			Eventually(func() string {
				return platform.Kubectl("get", "promiserevisions",
					"-l", fmt.Sprintf("kratix.io/promise-name=%s,kratix.io/latest-revision!=true", promiseName),
				)
			}).Should(SatisfyAll(
				ContainSubstring(initialPromiseVersion),
				Not(ContainSubstring(updatedPromiseVersion)),
			))

			Eventually(func() string {
				return platform.Kubectl("get", "promiserevisions",
					"-l", fmt.Sprintf("kratix.io/promise-name=%s,kratix.io/latest-revision=true", promiseName),
				)
			}).Should(SatisfyAll(
				ContainSubstring(updatedPromiseVersion),
				Not(ContainSubstring(initialPromiseVersion)),
			))
		})

		By("updating the resource request pinned to latest", func() {
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrOneName, "-ojsonpath='{.status.promiseVersion}'")
			}, time.Second*30).Should(ContainSubstring(updatedPromiseVersion))

			Eventually(func() string {
				return worker.Kubectl("get", "configmap")
			}).Should(ContainSubstring(rrOneAfterUpgradeCMName))

			Eventually(func(g Gomega) {
				name := getBindingName(promiseName, rrOneName)
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.status.lastAppliedVersion}'")).To(ContainSubstring(updatedPromiseVersion))
			}).Should(Succeed())
		})

		By("not updating the resource request pinned to a specific version", func() {
			Consistently(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName, "-ojsonpath='{.status.promiseVersion}'")
			}, time.Second*5).Should(ContainSubstring(initialPromiseVersion))

			Consistently(func() string {
				return worker.Kubectl("get", "configmap")
			}, time.Second*5).Should(ContainSubstring(rrTwoBeforeUpgradeCMName))

			Eventually(func(g Gomega) {
				name := getBindingName(promiseName, rrTwoName)
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.status.lastAppliedVersion}'")).To(ContainSubstring(initialPromiseVersion))
			}).Should(Succeed())
		})

		By("updating the resource binding to the new promise version", func() {
			bindingName := getBindingName(promiseName, rrTwoName)

			patch := []map[string]any{
				{
					"op":    "replace",
					"path":  "/spec/version",
					"value": updatedPromiseVersion,
				},
			}

			b, marshalErr := json.Marshal(patch)
			Expect(marshalErr).NotTo(HaveOccurred())
			platform.Kubectl("patch", bindingName, "--type=json", "-p", string(b))
			Eventually(func() string {
				return platform.Kubectl("get", "upgrades", rrTwoName, "-ojsonpath='{.status.promiseVersion}'")
			}).Should(ContainSubstring(updatedPromiseVersion))
		})

		By("upgrading the resource to the new promise version", func() {
			Eventually(func() string {
				return worker.Kubectl("get", "configmap")
			}).Should(SatisfyAll(
				ContainSubstring(rrTwoAfterUpgradeCMName),
				Not(ContainSubstring(rrTwoBeforeUpgradeCMName))))

			Eventually(func(g Gomega) {
				name := getBindingName(promiseName, rrTwoName)
				g.Expect(platform.Kubectl("get", "--namespace=default", name, "-o=jsonpath='{.status.lastAppliedVersion}'")).To(ContainSubstring(updatedPromiseVersion))
			}).Should(Succeed())
		})

		By("restoring the bindings correctly when they are deleted", func() {
			platform.EventuallyKubectlDelete("resourcebindings", "--all")

			Eventually(func(g Gomega) {
				resourceOneBindingName := getBindingName(promiseName, rrOneName)
				resourceTwoBindingName := getBindingName(promiseName, rrTwoName)

				g.Expect(platform.Kubectl("get", "--namespace=default", resourceOneBindingName, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring("latest"))
				g.Expect(platform.Kubectl("get", "--namespace=default", resourceTwoBindingName, "-o=jsonpath='{.spec.version}'")).To(ContainSubstring(updatedPromiseVersion))
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

func getBindingLabels(promiseName, resourceName string) map[string]string {
	GinkgoHelper()
	name := getBindingName(promiseName, resourceName)
	output := platform.Kubectl("get", "--namespace=default", name, "-o=json")
	var obj struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	Expect(json.Unmarshal([]byte(output), &obj)).To(Succeed())
	return obj.Metadata.Labels
}
