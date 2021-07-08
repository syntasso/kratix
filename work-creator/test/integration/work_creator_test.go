package integration_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	"github.com/syntasso/synpl-platform/work-creator/pipeline"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
)

var _ = Describe("WorkCreator", func() {

	BeforeSuite(func() {
		//Env Test
		By("bootstrapping test environment")
		testEnv = &envtest.Environment{
			CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
			ErrorIfCRDPathMissing: true,
		}

		cfg, err := testEnv.Start()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())

		//Setup the k8s client
		err = platformv1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient).NotTo(BeNil())

		//don't run main
		workCreator := pipeline.WorkCreator{
			K8sClient:  k8sClient,
			Identifier: getWorkResourceIdentifer(),
		}
		inputDirectory, err := filepath.Abs("samples")
		Expect(err).NotTo(HaveOccurred())
		workCreator.Execute(inputDirectory)
	}, 60)

	Describe("Work in the API server", func() {
		var work platformv1alpha1.Work
		BeforeEach(func() {
			work = getWork()
		})

		It("has a correctly configured Work resource", func() {
			Expect(work.Name).To(Equal(getWorkResourceIdentifer()))
			Expect(work.Spec.Workload.Manifests).To(HaveLen(1))
		})
	})

	AfterSuite(func() {
		By("tearing down the test environment")
		testEnv.Stop()
	})
})

func getWork() platformv1alpha1.Work {
	work := platformv1alpha1.Work{}
	expectedName := types.NamespacedName{
		Name:      getWorkResourceIdentifer(),
		Namespace: "default",
	}
	Expect(k8sClient).ToNot(BeNil())
	err := k8sClient.Get(context.Background(), expectedName, &work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

func getWorkResourceIdentifer() string {
	return "promise-targetnamespace-mydatabase"
}
