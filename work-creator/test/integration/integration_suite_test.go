package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

var _ = BeforeSuite(func() {
	//Env Test
	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	By("Initalise k8s client")
	err = v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	testEnv.Stop()
})

var _ = AfterEach(func() {
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Cluster{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Promise{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.Work{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &v1alpha1.WorkPlacement{}, client.InNamespace("default"))).To(Succeed())
})

func ContainManifest(expectedManifest unstructured.Unstructured) types.GomegaMatcher {
	return &ContainManifestMatcher{
		expectedManifest: expectedManifest,
	}
}

type ContainManifestMatcher struct {
	expectedManifest unstructured.Unstructured
}

func (matcher *ContainManifestMatcher) Match(actual interface{}) (bool, error) {
	expectedManifest := matcher.expectedManifest
	actualManifests, ok := actual.([]v1alpha1.Manifest)

	if !ok {
		return false, fmt.Errorf("Expected []v1alpha1.Manifest. Got\n%s", format.Object(actual, 1))
	}

	for _, actualManifest := range actualManifests {
		if actualManifest.GetName() == expectedManifest.GetName() {
			Expect(actualManifest.GetName()).To(Equal(expectedManifest.GetName()))
			Expect(actualManifest.GetNamespace()).To(Equal(expectedManifest.GetNamespace()))
			Expect(actualManifest.GetKind()).To(Equal(expectedManifest.GetKind()))
			return true, nil
		}
	}
	return false, nil
}

func (matcher *ContainManifestMatcher) FailureMessage(actual interface{}) string {
	return format.Message(actual, "to contain element matching", matcher.expectedManifest)
}

func (matcher *ContainManifestMatcher) NegatedFailureMessage(actual interface{}) string {
	return format.Message(actual, "not to contain element matching", matcher.expectedManifest)
}
