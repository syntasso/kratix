package system_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = FDescribe("Upgrade", func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	It("works", func() {
		promiseName := "upgrade"
		initialPromiseVersion := "v0.1.0"
		updatedPromiseVersion := "v0.2.0"
		rrName := "upgrade-rr"
		rrTwoName := "upgrade-rr-two"
		initialCMName := "upgrade-banana"
		//updatedCMName := "upgrade-banana-updated"
		twoCMName := "upgrade-apple-updated"

		By("creating a promise revision at promise installation")
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

		By("creating a resource binding when making a request")
		platform.Kubectl("apply", "-f", "assets/upgrades/resource-request.yaml")
		Eventually(func() string {
			return platform.Kubectl("get", "upgrades", rrName)
		}).Should(ContainSubstring("Reconciled"))

		resourceBindingLabels := strings.Join([]string{
			fmt.Sprintf("kratix.io/resource-name=%s", rrName),
			fmt.Sprintf("kratix.io/promise-name=%s", promiseName),
		}, ",")

		Eventually(func() string {
			return platform.Kubectl("get", "--namespace=default",
				"resourcebindings", "-l", resourceBindingLabels, "-o=name")
		}).Should(ContainSubstring(fmt.Sprintf("%s-%s", rrName, promiseName)))

		Eventually(func() string {
			return worker.Kubectl("get", "configmap")
		}).Should(ContainSubstring(initialCMName))

		By("creating a new promise revision when a new promise version is installed")
		Eventually(func() string {
			return platform.Kubectl("get", "promiserevisions")
		}).Should(SatisfyAll(
			ContainSubstring(fmt.Sprintf("%s-%s", promiseName, initialPromiseVersion)),
			ContainSubstring(fmt.Sprintf("%s-%s", promiseName, updatedPromiseVersion))))

		By("not updating the existing resource request")
		platform.Kubectl("apply", "-f", "assets/upgrades/promise-new-version.yaml")
		Eventually(func() string {
			return platform.Kubectl("get", "promise", promiseName)
		}).Should(SatisfyAll(
			ContainSubstring("Available"),
			ContainSubstring(updatedPromiseVersion)))

		Consistently(func() string {
			return platform.Kubectl("get", "upgrades", rrName, "-ojsonpath='{.status.promiseVersion}'")
		}, time.Second*5).Should(ContainSubstring(initialPromiseVersion))

		Consistently(func() string {
			return worker.Kubectl("get", "configmap")
		}, time.Second*5).Should(ContainSubstring(initialCMName))

		By("reconciling a new resource request at the latest revision")
		platform.Kubectl("apply", "-f", "assets/upgrades/resource-request-2.yaml")
		Eventually(func() string {
			return platform.Kubectl("get", "upgrades", rrTwoName)
		}).Should(ContainSubstring("Reconciled"))

		Eventually(func() string {
			return worker.Kubectl("get", "configmap")
		}).Should(ContainSubstring(twoCMName))

		By("being able to upgrade the resource request when updating the resource binding")

	})
})
