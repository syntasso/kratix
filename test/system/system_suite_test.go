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
	worker   *kubeutils.Cluster
	platform *kubeutils.Cluster
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

	platform.Kubectl("apply", "-f", "./assets/kratix-config.yaml")
	platform.Kubectl("delete", "pod", "-l", "control-plane=controller-manager", "-n", "kratix-platform-system")
	platform.Kubectl("wait", "-n", "kratix-platform-system", "deployments", "-l", "control-plane=controller-manager", "--for=condition=Available")
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
})

func getEnvOrDefault(envVar, defaultValue string) string {
	value := os.Getenv(envVar)
	if value == "" {
		return defaultValue
	}
	return value
}
