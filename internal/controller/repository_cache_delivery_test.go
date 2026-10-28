package controller_test

import (
	"context"

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
