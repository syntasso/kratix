package system_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	k8sClient   client.Client
	testTempDir string
)

func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = BeforeSuite(func() {
	var err error
	testTempDir, err = os.MkdirTemp(os.TempDir(), "systest")
	Expect(err).NotTo(HaveOccurred())
	initK8sClient()
	tmpDir, err := os.MkdirTemp(os.TempDir(), "systest")
	Expect(err).NotTo(HaveOccurred())
	platform.kubectl("apply", "-f", catAndReplaceFluxResources(tmpDir, "./assets/git/platform_gitops-tk-resources.yaml"))
	platform.kubectl("apply", "-f", catAndReplaceFluxResources(tmpDir, "./assets/git/platform_statestore.yaml"))
	platform.kubectl("apply", "-f", catAndReplaceFluxResources(tmpDir, "./assets/git/platform_kratix_destination.yaml"))
	os.RemoveAll(tmpDir)
})

var _ = AfterSuite(func() {
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

func catAndReplacePromiseRelease(tmpDir, file, port, bashPromiseName string) string {
	bytes, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())
	//Set via the Makefile
	hostIP := "host.docker.internal"
	if runtime.GOOS == "linux" {
		hostIP = "172.17.0.1"
	}
	output := strings.ReplaceAll(string(bytes), "LOCALHOST", hostIP)
	output = strings.ReplaceAll(output, "REPLACEPORT", port)
	output = strings.ReplaceAll(output, "REPLACEBASH", bashPromiseName)
	tmpFile := filepath.Join(tmpDir, filepath.Base(file))
	err = os.WriteFile(tmpFile, []byte(output), 0777)
	Expect(err).NotTo(HaveOccurred())
	return tmpFile
}

func initK8sClient() {
	cfg := ctrl.GetConfigOrDie()

	Expect(platformv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(apiextensionsv1.AddToScheme(scheme.Scheme)).To(Succeed())
	var err error
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
}
