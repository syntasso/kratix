package delivery_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers"
)

// Each spec registers its own mode: the registry is process-wide and refuses a second
// registration, so a shared name would make specs depend on their order.
var _ = Describe("Delivery registry", func() {
	stubFactory := func(string) delivery.Factory {
		return func(base writers.StateStoreWriter, _ map[string]string) (writers.StateStoreWriter, error) {
			return base, nil
		}
	}

	Describe("For", func() {
		It("returns no factory for the built-in mode, so the state store's own writer is used", func() {
			factory, err := delivery.For(delivery.BuiltIn)

			Expect(err).NotTo(HaveOccurred())
			Expect(factory).To(BeNil())
		})

		It("names the mode nobody registered, and what this build does register", func() {
			delivery.Register("for-names-others", stubFactory("for-names-others"))

			_, err := delivery.For("pullRequest")

			Expect(err).To(MatchError(ContainSubstring(`no writer registered for delivery mode "pullRequest"`)))
			Expect(err).To(MatchError(ContainSubstring("for-names-others")))
		})

		It("returns the registered factory", func() {
			delivery.Register("for-returns", stubFactory("for-returns"))

			factory, err := delivery.For("for-returns")

			Expect(err).NotTo(HaveOccurred())
			Expect(factory).NotTo(BeNil())
		})
	})

	Describe("Register", func() {
		It("refuses the built-in mode, which would redirect every existing state store", func() {
			Expect(func() { delivery.Register(delivery.BuiltIn, stubFactory("")) }).
				To(PanicWith(ContainSubstring("cannot register the built-in mode")))
		})

		It("refuses a second registration for one mode, rather than letting the last one win", func() {
			delivery.Register("register-twice", stubFactory("register-twice"))

			Expect(func() { delivery.Register("register-twice", stubFactory("register-twice")) }).
				To(PanicWith(ContainSubstring(`mode "register-twice" is already registered`)))
		})

		It("refuses a nil factory, which would panic later at the write instead", func() {
			Expect(func() { delivery.Register("register-nil", nil) }).
				To(PanicWith(ContainSubstring("nil factory")))
		})
	})
})
