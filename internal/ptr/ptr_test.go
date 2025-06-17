package ptr_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/internal/ptr"
)

var _ = Describe("ptr", func() {
	Describe("To", func() {
		It("returns a pointer to a copy of the input", func() {
			By("checking the dereferenced value")
			v := 5
			vp := ptr.To(v)
			Expect(*vp).To(Equal(5))

			By("checking immutability of the original")
			*vp = 4 // updates a copy of the original
			Expect(v).To(Equal(5))

			By("checking with a different type")
			s := "Hello, world!"
			Expect(*ptr.To(s)).To(Equal("Hello, world!"))
		})

	})

	Describe("True()", func() {
		It("is true", func() {
			Expect(*ptr.True()).To(BeTrue())
		})

		It("produces independent values", func() {
			v := ptr.True()
			*v = false
			Expect(*ptr.True()).To(BeTrue())
		})
	})

	Describe("False()", func() {
		It("is false", func() {
			Expect(*ptr.False()).To(BeFalse())
		})

		It("produces independent values", func() {
			v := ptr.False()
			*v = true
			Expect(*ptr.False()).To(BeFalse())
		})
	})
})
