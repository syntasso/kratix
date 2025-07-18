package lib_test

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Work Creator")
}

var (
	k8sClient client.Client
)

var _ = BeforeSuite(func(_ SpecContext) {
	err := v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

}, NodeTimeout(time.Minute))

var _ = BeforeEach(func() {
	k8sClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).WithStatusSubresource(
		&v1alpha1.PromiseRelease{},
		&v1alpha1.Promise{},
		&v1alpha1.Work{},
		&v1alpha1.WorkPlacement{},
		&v1alpha1.Destination{},
		&v1alpha1.GitStateStore{},
		&v1alpha1.BucketStateStore{},
	).Build()
})
