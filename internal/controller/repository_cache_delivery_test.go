package controller_test

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
)

var _ = Describe("writerForDelivery", func() {
	var base *writersfakes.FakeStateStoreWriter

	BeforeEach(func() {
		base = &writersfakes.FakeStateStoreWriter{}
	})

	storeWith := func(spec *v1alpha1.Delivery) *v1alpha1.GitStateStore {
		store := &v1alpha1.GitStateStore{}
		store.Spec.Delivery = spec
		return store
	}

	expectWritesThroughBase := func(writer delivery.Writer) {
		base.UpdateFilesReturns("v1", nil)
		result, err := writer.Deliver(context.Background(), delivery.Target{Dir: "d/", WorkPlacement: "wp"}, nil, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(delivery.Result{Outcome: delivery.Delivered, VersionID: "v1"}))
		Expect(base.UpdateFilesCallCount()).To(Equal(1))
	}

	It("returns the built-in writer over the store's default writer when the delivery field is not set", func() {
		writer, err := controller.WriterForDelivery(storeWith(nil), base, nil)

		Expect(err).ToNot(HaveOccurred())
		expectWritesThroughBase(writer)
	})

	It("returns the built-in writer over the store's default writer when the delivery field is empty", func() {
		writer, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{}), base, nil)

		Expect(err).ToNot(HaveOccurred())
		expectWritesThroughBase(writer)
	})

	It("errors when the delivery.mode is unknown", func() {
		_, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{Mode: "unclaimed-mode"}), base, nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unclaimed-mode"))
	})

	It("returns the custom writer when the delivery.mode is valid", func() {
		registered := &fakeDeferredWriter{FakeStateStoreWriter: &writersfakes.FakeStateStoreWriter{}}
		var gotBase writers.StateStoreWriter
		var gotConfig map[string]string

		delivery.Register("cache-registered", func(b writers.StateStoreWriter, c map[string]string, _ map[string][]byte) (delivery.Writer, error) {
			gotBase, gotConfig = b, c
			return registered, nil
		})

		writer, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{
			Mode:   "cache-registered",
			Config: map[string]string{"provider": "github"},
		}), base, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(writer).To(BeIdenticalTo(delivery.Writer(registered)))
		Expect(gotBase).To(BeIdenticalTo(writers.StateStoreWriter(base)))
		Expect(gotConfig).To(Equal(map[string]string{"provider": "github"}))
	})

	It("works the same for a bucket store, which shares the delivery block", func() {
		store := &v1alpha1.BucketStateStore{}
		store.Spec.Delivery = &v1alpha1.Delivery{Mode: "unclaimed-bucket-mode"}

		_, err := controller.WriterForDelivery(store, base, nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unclaimed-bucket-mode"))
	})
})

var _ = Describe("InitRepository", func() {
	initBucketRepository := func(spec *v1alpha1.Delivery) *controller.Repository {
		controller.SetNewS3Writer(func(logr.Logger, v1alpha1.BucketStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error) {
			return &writersfakes.FakeStateStoreWriter{}, nil
		})
		DeferCleanup(controller.SetNewS3Writer, writers.NewS3Writer)

		store := &v1alpha1.BucketStateStore{}
		store.SetName("mode-store")
		store.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind(v1alpha1.BucketStateStoreKind))
		store.Spec.Delivery = spec

		repo, err := controller.NewRepositoryCache().InitRepository(logr.Discard(), store, corev1.Secret{})
		Expect(err).ToNot(HaveOccurred())
		return repo
	}

	It("records the delivery mode the writer was built for", func() {
		delivery.Register("mode-recorded", func(b writers.StateStoreWriter, _ map[string]string, _ map[string][]byte) (delivery.Writer, error) {
			return delivery.Direct(b), nil
		})

		Expect(initBucketRepository(&v1alpha1.Delivery{Mode: "mode-recorded"}).DeliveryMode).To(Equal("mode-recorded"))
	})
})
