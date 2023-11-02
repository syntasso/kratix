package system_test

import (
	"fmt"
	"os"
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

	platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_gitops-tk-resources.yaml", storeType))
	platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_statestore.yaml", storeType))
	platform.kubectl("apply", "-f", fmt.Sprintf("./assets/%s/platform_kratix_destination.yaml", storeType))
})

func initK8sClient() {
	cfg := ctrl.GetConfigOrDie()

	Expect(platformv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(apiextensionsv1.AddToScheme(scheme.Scheme)).To(Succeed())
	var err error
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
}
