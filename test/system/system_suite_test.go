package system_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	testTempDir string
)

func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = SynchronizedBeforeSuite(func() {
	//this runs once for the whole suite
	worker = &destination{
		context: getEnvOrDefault("WORKER_CONTEXT", "kind-worker"),
		name:    getEnvOrDefault("WORKER_NAME", "worker-1"),
	}
	platform = &destination{
		context: getEnvOrDefault("PLATFORM_CONTEXT", "kind-platform"),
		name:    getEnvOrDefault("PLATFORM_NAME", "platform-cluster"),
	}

	platform.kubectl("apply", "-f", "./assets/kratix-config.yaml")
	platform.kubectl("delete", "pod", "-l", "control-plane=controller-manager", "-n", "kratix-platform-system")
	platform.kubectl("wait", "-n", "kratix-platform-system", "deployments", "-l", "control-plane=controller-manager", "--for=condition=Available")

	var err error
	testTempDir, err = os.MkdirTemp(os.TempDir(), "systest")
	Expect(err).NotTo(HaveOccurred())
	tmpDir, err := os.MkdirTemp(os.TempDir(), "systest")
	Expect(err).NotTo(HaveOccurred())

	if getEnvOrDefault("PLATFORM_SKIP_SETUP", "false") == "true" {
		// Useful when running tests against a non-kind cluster
		// Files applied below are configured for local development
		return
	}

	platform.kubectl("apply", "-f", "../../hack/destination/gitops-tk-install.yaml")
	platform.kubectl("apply", "-f", catAndReplaceFluxResources(tmpDir, "./assets/git/platform_gitops-tk-resources.yaml"))
	platform.kubectl("apply", "-f", catAndReplaceFluxResources(tmpDir, "./assets/git/destinations.yaml"))
	os.RemoveAll(tmpDir)
}, func() {
	//this runs before each test

	//These variables get set in func above, but only for 1 of the nodes, so we set
	//them again here to ensure all nodes have them
	worker = &destination{
		context: getEnvOrDefault("WORKER_CONTEXT", "kind-worker"),
		name:    getEnvOrDefault("WORKER_NAME", "worker-1"),
	}
	platform = &destination{
		context: getEnvOrDefault("PLATFORM_CONTEXT", "kind-platform"),
		name:    getEnvOrDefault("PLATFORM_NAME", "platform-cluster"),
	}

	endpoint = getEnvOrDefault("BUCKET_ENDPOINT", "localhost:31337")
	secretAccessKey = getEnvOrDefault("BUCKET_SECRET_KEY", "minioadmin")
	accessKeyID = getEnvOrDefault("BUCKET_ACCESS_KEY", "minioadmin")
	useSSL = os.Getenv("BUCKET_SSL") == "true"
	bucketName = getEnvOrDefault("BUCKET_NAME", "kratix")
})

func getEnvOrDefault(envVar, defaultValue string) string {
	value := os.Getenv(envVar)
	if value == "" {
		return defaultValue
	}
	return value
}

var _ = SynchronizedAfterSuite(func() {}, func() {
	platform.eventuallyKubectlDelete("deployments", "-n", "kratix-platform-system", "kratix-promise-release-test-hoster")
	os.RemoveAll(testTempDir)
})

func catAndReplaceFluxResources(tmpDir, file string) string {
	bytes, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())
	//Set via the Makefile
	ip := os.Getenv("PLATFORM_DESTINATION_IP")
	hostIP := "host.docker.internal"
	if runtime.GOOS == "linux" {
		hostIP = "172.17.0.1"
	}
	output := strings.ReplaceAll(string(bytes), "PLACEHOLDER", ip)
	output = strings.ReplaceAll(output, "LOCALHOST", hostIP)
	tmpFile := filepath.Join(tmpDir, filepath.Base(file))
	err = os.WriteFile(tmpFile, []byte(output), 0777)
	Expect(err).NotTo(HaveOccurred())
	return tmpFile
}
