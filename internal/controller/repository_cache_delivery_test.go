package controller_test

import (
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

	It("keeps the store's own writer when there is no delivery block", func() {
		writer, err := controller.WriterForDelivery(storeWith(nil), base, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(writer).To(BeIdenticalTo(writers.StateStoreWriter(base)))
	})

	It("keeps the store's own writer when the mode is empty", func() {
		writer, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{}), base, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(writer).To(BeIdenticalTo(writers.StateStoreWriter(base)))
	})

	It("names the mode when this build registers no writer for it", func() {
		_, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{Mode: "unclaimed-mode"}), base, nil)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unclaimed-mode"))
	})

	It("hands the registered writer the store's writer and its config", func() {
		registered := &writersfakes.FakeStateStoreWriter{}
		var gotBase writers.StateStoreWriter
		var gotConfig map[string]string

		delivery.Register("cache-registered", func(b writers.StateStoreWriter, c map[string]string, _ map[string][]byte) (writers.StateStoreWriter, error) {
			gotBase, gotConfig = b, c
			return registered, nil
		})

		writer, err := controller.WriterForDelivery(storeWith(&v1alpha1.Delivery{
			Mode:   "cache-registered",
			Config: map[string]string{"provider": "github"},
		}), base, nil)

		Expect(err).ToNot(HaveOccurred())
		Expect(writer).To(BeIdenticalTo(writers.StateStoreWriter(registered)))
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
