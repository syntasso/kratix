package system_test

import (
	"os"
	"testing"
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	worker           *kubeutils.Cluster
	platform         *kubeutils.Cluster
	kratixConfigPath string
)

func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = SynchronizedBeforeSuite(func() {

	//this runs once for the whole suite
	platform = &kubeutils.Cluster{
		Context: getEnvOrDefault("PLATFORM_CONTEXT", "kind-platform"),
		Name:    getEnvOrDefault("PLATFORM_NAME", "platform-cluster")}
	worker = &kubeutils.Cluster{
		Context: getEnvOrDefault("WORKER_CONTEXT", "kind-worker"),
		Name:    getEnvOrDefault("WORKER_NAME", "worker-1")}

	kubeutils.SetTimeoutAndInterval(30*time.Second, 2*time.Second)
	kratixConfigPath = "./assets/kratix-config.yaml"

	platform.Kubectl("apply", "-f", kratixConfigPath)
	// The manager ships with a 100m CPU / 256Mi limit, which throttles it badly
	// under the suite's parallel load. Give it headroom for the tests.
	platform.Kubectl("patch", "deployment", "kratix-platform-controller-manager",
		"-n", "kratix-platform-system", "--type=strategic", "-p",
		`{"spec":{"template":{"spec":{"containers":[{"name":"manager","resources":{"limits":{"cpu":"2","memory":"1Gi"},"requests":{"cpu":"200m","memory":"256Mi"}}}]}}}}`)
	// The patch rolls the deployment; this polls the webhook until it answers.
	restartController()

}, func() {
	//this runs before each test

	//These variables get set in func above, but only for 1 of the nodes, so we set
	//them again here to ensure all nodes have them
	platform = &kubeutils.Cluster{
		Context: getEnvOrDefault("PLATFORM_CONTEXT", "kind-platform"),
		Name:    getEnvOrDefault("PLATFORM_NAME", "platform-cluster")}
	worker = &kubeutils.Cluster{
		Context: getEnvOrDefault("WORKER_CONTEXT", "kind-worker"),
		Name:    getEnvOrDefault("WORKER_NAME", "worker-1")}
	kratixConfigPath = "./assets/kratix-config.yaml"
})

// Config-mutating specs leave their config behind, so restore the default here.
// No restart: it would replace the pod CI collects the failure logs from.
var _ = SynchronizedAfterSuite(func() {}, func() {
	platform.Kubectl("apply", "-f", kratixConfigPath)
})

func getEnvOrDefault(envVar, defaultValue string) string {
	value := os.Getenv(envVar)
	if value == "" {
		return defaultValue
	}
	return value
}
