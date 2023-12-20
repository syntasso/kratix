package pipeline_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
	err       error
)

var _ = BeforeSuite(func(_ SpecContext) {
	//Env Test
	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	By("Initialise k8s client")
	err = v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

}, NodeTimeout(time.Minute))

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	testEnv.Stop()
})

var _ = AfterEach(func() {
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Destination{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Promise{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Work{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.WorkPlacement{}, client.InNamespace("default"))).To(Succeed())
})
