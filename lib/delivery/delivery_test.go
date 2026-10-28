package delivery_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers"
)

// Each spec registers its own mode: the registry is process-wide and refuses a second one.
var _ = Describe("Delivery registry", func() {
	stubFactory := func(string) delivery.Factory {
		return func(base writers.StateStoreWriter, _ map[string]string,
			_ map[string][]byte) (delivery.Writer, error) {
			return delivery.Direct(base), nil
		}
	}

	Describe("For", func() {
		It("returns a factory for the built-in mode, so every state store gets a delivery writer", func() {
			factory, err := delivery.For(delivery.BuiltIn)

			Expect(err).NotTo(HaveOccurred())
			Expect(factory).NotTo(BeNil())
		})

		It("errors when called with an unregistered delivery mode", func() {
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

		It("refuses a second registration for one mode", func() {
			delivery.Register("register-twice", stubFactory("register-twice"))

			Expect(func() { delivery.Register("register-twice", stubFactory("register-twice")) }).
				To(PanicWith(ContainSubstring(`mode "register-twice" is already registered`)))
		})

		It("refuses a nil factory", func() {
			Expect(func() { delivery.Register("register-nil", nil) }).
				To(PanicWith(ContainSubstring("nil factory")))
		})
	})
})
