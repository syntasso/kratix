package system_test

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

// This spec covers the featureFlags.dryRun wiring rather than dry-run behaviour: the
// flag is set suite-wide in assets/kratix-config.yaml, and status on the DryRun is the
// proof the controller it gates is actually running.
var _ = Describe("Dry Run feature flag", func() {
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
		if !CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.KubectlAllowFail("get", "--namespace=default", "dryrun", dryRunName, "-o=yaml")
			platform.KubectlAllowFail("get", "--namespace=default", "dryrunflags", "-l", "kratix.io/dry-run-owner="+dryRunName, "-o=yaml")
			platform.KubectlAllowFail("get", "--namespace=default", "jobs,pods", "-l", "kratix.io/promise-name="+promiseName, "-o=wide")
			platform.KubectlAllowFail("get", "--namespace=default", "works", "-l", "kratix.io/promise-name="+promiseName, "-o=jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{.metadata.labels}{\"\\n\"}{.status}{\"\\n\\n\"}{end}")
			platform.KubectlAllowFail("get", "--namespace=default", "workplacements", "-l", "kratix.io/targetDestinationName="+destinationName, "-o=jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{.status}{\"\\n\\n\"}{end}")
			platform.KubectlAllowFail("get", "destination", destinationName, "-o=yaml")
			printControllerLogsMentioning(promiseName, dryRunName, destinationName)
		}

		platform.EventuallyKubectlDelete("--namespace=default", "dryrun", dryRunName)
		platform.EventuallyKubectlDelete("promise", promiseName)
		platform.EventuallyKubectlDelete("destination", destinationName)
	})

	It("runs the DryRun controller, which reports completion on the DryRun", func() {
		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "dry-run.yaml"))

		Eventually(func() string {
			status := platform.Kubectl("get", "--namespace=default", "dryrun", dryRunName,
				"-o=jsonpath={.status.conditions[?(@.type=='Completed')].status}")
			if status == "False" {
				StopTrying("the DryRun failed: " + platform.Kubectl("get", "--namespace=default", "dryrun", dryRunName,
					"-o=jsonpath={.status.conditions[?(@.type=='Completed')].message}")).Now()
			}
			return status
		}).Should(Equal("True"))
	})
})

// The controller log covers every spec running in parallel, so only the lines
// about this spec's objects are printed.
func printControllerLogsMentioning(names ...string) {
	out, err := exec.Command("kubectl", "logs", "--context="+platform.Context,
		"--namespace=kratix-platform-system", "deployment/kratix-platform-controller-manager",
		"--container=manager", "--since=10m").CombinedOutput()
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "could not read controller logs: %v\n%s\n", err, out)
		return
	}
	fmt.Fprintln(GinkgoWriter, "controller logs mentioning", names)
	for _, line := range strings.Split(string(out), "\n") {
		for _, name := range names {
			if strings.Contains(line, name) {
				fmt.Fprintln(GinkgoWriter, line)
				break
			}
		}
	}
}
