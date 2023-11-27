package system_test

import (
	"fmt"
	"os"
	"path/filepath"
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
	k8sClient client.Client
)

func TestSystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "System Suite")
}

var _ = BeforeSuite(func() {
	initK8sClient()
	storeType = "bucket"
	if os.Getenv("SYSTEM_TEST_STORE_TYPE") == "git" {
		storeType = "git"
	}
	fmt.Println("Running system tests with statestore " + storeType)

	tmpDir, err := os.MkdirTemp(os.TempDir(), "systest")
	Expect(err).NotTo(HaveOccurred())
	platform.kubectl("apply", "-f", catAndReplace(tmpDir, fmt.Sprintf("./assets/%s/platform_gitops-tk-resources.yaml", storeType)))
	platform.kubectl("apply", "-f", catAndReplace(tmpDir, fmt.Sprintf("./assets/%s/platform_statestore.yaml", storeType)))
	platform.kubectl("apply", "-f", catAndReplace(tmpDir, fmt.Sprintf("./assets/%s/platform_kratix_destination.yaml", storeType)))
	os.RemoveAll(tmpDir)
})

func catAndReplace(tmpDir, file string) string {
	bytes, err := os.ReadFile(file)
	Expect(err).NotTo(HaveOccurred())
	//Set via the Makefile
	ip := os.Getenv("PLATFORM_DESTINATION_IP")
	output := strings.ReplaceAll(string(bytes), "PLACEHOLDER", ip)
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
