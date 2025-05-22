package installer_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"fmt"
	"os/exec"
	"strings"
	"time"
)

var _ = Describe("Kratix Quick Start Installer", func() {
	const (
		clusterName  = "kratix-test"
		jobManifest  = "../manifests/kratix-quick-start-installer.yaml"
		promiseURL   = "https://raw.githubusercontent.com/syntasso/kratix-marketplace/refs/heads/main/namespace/promise.yaml"
		requestURL   = "https://raw.githubusercontent.com/syntasso/kratix-marketplace/refs/heads/main/namespace/resource-request.yaml"
		jobName      = "kratix-quick-start-installer"
		nsPlatform   = "kratix-platform-system"
		nsWorker     = "kratix-worker-system"
		promiseCRD   = "namespaces.marketplace.kratix.io"
		resourceKind = "namespace"
		resourceName = "promised-namespace"
		resourceNS   = "promised-namespace"
	)

	It("installs Kratix and provisions a namespaced resource", func() {
		By("Step 1: Applying Kratix installer job")
		runKubectl("apply", "-f", jobManifest)

		By("Step 2: Waiting for installer Job to complete")
		runKubectl("wait", "--for=condition=complete", fmt.Sprintf("job/%s", jobName), "--timeout=10m")

		By("Step 3: Verifying Kratix components")
		runKubectl("get", "namespace", nsPlatform)
		runKubectl("get", "namespace", nsWorker)

		By("Step 4: Installing Namespace Promise")
		runKubectl("apply", "-f", promiseURL)

		By("Step 5: Waiting for Promise CRD to be available")
		time.Sleep(10 * time.Second)
		assertCRDExists(promiseCRD)

		By("Step 6: Requesting a Namespace instance")
		runKubectl("apply", "-f", requestURL)

		By("Step 7: Waiting for promised namespace to be created")
		time.Sleep(20 * time.Second)
		eventuallyResourceExists(resourceKind, resourceName, resourceNS)

		By("Step 8: Cleaning up")
		runKubectl("delete", "-f", promiseURL)
	})
})

func runKubectl(args ...string) string {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), fmt.Sprintf("kubectl %s\nOutput: %s", strings.Join(args, " "), out))
	return string(out)
}

func eventuallyResourceExists(kind, name, namespace string) {
	EventuallyWithOffset(1, func() error {
		cmd := exec.Command("kubectl", "get", kind, name, "-n", namespace)
		return cmd.Run()
	}, 60*time.Second, 10*time.Second).Should(Succeed(), fmt.Sprintf("Expected %s/%s in namespace %s", kind, name, namespace))
}

func assertCRDExists(crd string) {
	By(fmt.Sprintf("Asserting CRD '%s' exists", crd))
	runKubectl("get", "crd", crd)
}
