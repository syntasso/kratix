package controller_test

import (
	"os"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
)

var _ = Describe("RepositoryCache Cleanup", func() {
	var (
		cache      controller.RepositoryCache
		fakeWriter *writersfakes.FakeStateStoreWriter
		cloneDir   string
	)

	gitStore := func(name string) *v1alpha1.GitStateStore {
		return &v1alpha1.GitStateStore{
			TypeMeta:   metav1.TypeMeta{Kind: v1alpha1.GitStateStoreKind},
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}
	bucketStore := func(name string) *v1alpha1.BucketStateStore {
		return &v1alpha1.BucketStateStore{
			TypeMeta:   metav1.TypeMeta{Kind: v1alpha1.BucketStateStoreKind},
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
	}

	BeforeEach(func() {
		cache = controller.NewRepositoryCache()
		fakeWriter = &writersfakes.FakeStateStoreWriter{}

		var err error
		cloneDir, err = os.MkdirTemp("", "repository-cache-test")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, cloneDir)
		fakeWriter.InitReturns(cloneDir, nil)

		controller.SetNewGitWriter(func(logr.Logger, v1alpha1.GitStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error) {
			return fakeWriter, nil
		})
		controller.SetNewS3Writer(func(logr.Logger, v1alpha1.BucketStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error) {
			return fakeWriter, nil
		})
	})

	initRepository := func(store controller.StateStore) *controller.Repository {
		repo, err := cache.InitRepository(logr.Discard(), store, corev1.Secret{})
		Expect(err).ToNot(HaveOccurred())
		return repo
	}

	cleanupInBackground := func(store controller.StateStore) chan error {
		done := make(chan error, 1)
		go func() { done <- cache.Cleanup(store) }()
		return done
	}

	DescribeTable("a repository a reconcile got before Cleanup refuses writes once Cleanup returns",
		func(store controller.StateStore, kind string) {
			repo := initRepository(store)

			Expect(cache.Cleanup(store)).To(Succeed())

			repo.Lock()
			defer repo.Unlock()
			Expect(repo.Writer.Reset()).To(MatchError(ContainSubstring("cleaned up")))
			Expect(fakeWriter.ResetCallCount()).To(Equal(0))

			_, err := cache.GetRepositoryByTypeAndName(kind, store.GetName())
			Expect(err).To(MatchError(controller.ErrCacheMiss))
		},
		Entry("git", gitStore("store"), v1alpha1.GitStateStoreKind),
		Entry("bucket", bucketStore("store"), v1alpha1.BucketStateStoreKind),
	)

	It("deletes the git clone", func() {
		initRepository(gitStore("store"))

		Expect(cache.Cleanup(gitStore("store"))).To(Succeed())

		Expect(cloneDir).NotTo(BeADirectory())
	})

	DescribeTable("waits for a delivery holding the repository lock to finish",
		func(store controller.StateStore) {
			repo := initRepository(store)
			repo.Lock()

			done := cleanupInBackground(store)

			Consistently(done, 200*time.Millisecond).ShouldNot(Receive())
			Expect(repo.Writer.Reset()).To(Succeed())

			repo.Unlock()
			Eventually(done).Should(Receive(BeNil()))
		},
		Entry("git", gitStore("store")),
		Entry("bucket", bucketStore("store")),
	)

	It("does not block other state stores while it waits on a repository lock", func() {
		repo := initRepository(gitStore("waiting"))
		initRepository(gitStore("other"))
		repo.Lock()
		DeferCleanup(repo.Unlock)

		done := cleanupInBackground(gitStore("waiting"))
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())

		lookup := make(chan error, 1)
		go func() {
			_, err := cache.GetRepositoryByTypeAndName(v1alpha1.GitStateStoreKind, "other")
			lookup <- err
		}()
		Eventually(lookup, time.Second).Should(Receive(BeNil()))
	})
})
