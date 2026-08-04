package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

// This spec covers the featureFlags.dryRun wiring rather than dry-run behaviour: the
// flag is set suite-wide in assets/kratix-config.yaml, and status on the DryRun is the
// proof the controller it gates is actually running.
var _ = Describe("Dry Run feature flag", Serial, func() {
	const (
		assetsPath      = "assets/dry-run-feature-flag"
		promiseName     = "dryrunflag"
		dryRunName      = "flag-preview"
		destinationName = "dry-run-flag-test"
	)

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "destination.yaml"))
		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise.yaml"))
		Eventually(func() string {
			return platform.Kubectl("get", "promise", promiseName)
		}).Should(ContainSubstring("Available"))
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete("--namespace=default", "dryrun", dryRunName)
		platform.EventuallyKubectlDelete("promise", promiseName)
		platform.EventuallyKubectlDelete("destination", destinationName)
	})

	It("runs the DryRun controller, which reports completion on the DryRun", func() {
		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "dry-run.yaml"))

		Eventually(func() string {
			return platform.Kubectl("get", "--namespace=default", "dryrun", dryRunName,
				"-o=jsonpath={.status.conditions[?(@.type=='Completed')].status}")
		}).Should(Equal("True"))
	})
})
