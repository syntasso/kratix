package migrations_test

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	fakeK8sClient client.Client
	ctx           = context.TODO()
	logger        logr.Logger
)

func TestManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Migration Suite")
}

var _ = BeforeSuite(func() {
	err := v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	//+kubebuilder:scaffold:scheme

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	fakeK8sClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).WithStatusSubresource(
		&v1alpha1.Promise{},
	).Build()
})

var _ = BeforeEach(func() {
	fakeK8sClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).WithStatusSubresource(
		&v1alpha1.Promise{},
	).Build()
	logger = ctrl.Log.WithName("manager")
})
