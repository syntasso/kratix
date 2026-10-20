package writers_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

var _ = Describe("NewGitExecutor", func() {
	var (
		logger         logr.Logger
		stateStoreSpec v1alpha1.GitStateStoreSpec
		creds          map[string][]byte
	)

	BeforeEach(func() {
		logger = ctrl.Log.WithName("executor")
		stateStoreSpec = v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path:      "state-store-path",
				SecretRef: &corev1.SecretReference{Namespace: "default", Name: "dummy-secret"},
			},
			AuthMethod: "basicAuth",
			URL:        "https://github.com/syntasso/kratix",
			Branch:     "test",
		}
		creds = map[string][]byte{
			"username": []byte("user1"),
			"password": []byte("pw1"),
		}
	})

	It("returns an executor for the state store's repository", func() {
		executor, err := writers.NewGitExecutor(logger, stateStoreSpec, "state-store-path/dst", creds)
		Expect(err).NotTo(HaveOccurred())
		Expect(executor).NotTo(BeNil())
	})

	It("returns an error when the credentials do not match the auth method", func() {
		_, err := writers.NewGitExecutor(logger, stateStoreSpec, "state-store-path/dst", map[string][]byte{})
		Expect(err).To(MatchError(ContainSubstring("could not create auth method")))
	})
})
