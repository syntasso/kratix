package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("StateStoreReference", func() {
	Describe("GroupVersionKind", func() {
		It("defaults the api version to the Kratix platform group when it is not set", func() {
			ref := v1alpha1.StateStoreReference{Kind: "GitStateStore", Name: "store"}
			Expect(ref.GroupVersionKind()).To(Equal(schema.GroupVersionKind{
				Group:   "platform.kratix.io",
				Version: "v1alpha1",
				Kind:    "GitStateStore",
			}))
		})

		It("uses the api version when it is set", func() {
			ref := v1alpha1.StateStoreReference{
				APIVersion: "platform.syntasso.io/v1alpha1",
				Kind:       "EnterpriseStateStore",
				Name:       "store",
			}
			Expect(ref.GroupVersionKind()).To(Equal(schema.GroupVersionKind{
				Group:   "platform.syntasso.io",
				Version: "v1alpha1",
				Kind:    "EnterpriseStateStore",
			}))
		})
	})

	Describe("IsManagedByCustomController", func() {
		It("is false for a GitStateStore with no api version", func() {
			ref := v1alpha1.StateStoreReference{Kind: "GitStateStore", Name: "store"}
			Expect(ref.IsManagedByCustomController()).To(BeFalse())
		})

		It("is false for a BucketStateStore in the Kratix platform group", func() {
			ref := v1alpha1.StateStoreReference{
				APIVersion: "platform.kratix.io/v1alpha1",
				Kind:       "BucketStateStore",
				Name:       "store",
			}
			Expect(ref.IsManagedByCustomController()).To(BeFalse())
		})

		It("is true for a kind Kratix does not know", func() {
			ref := v1alpha1.StateStoreReference{Kind: "EnterpriseStateStore", Name: "store"}
			Expect(ref.IsManagedByCustomController()).To(BeTrue())
		})

		It("is true for a known kind in another api group", func() {
			ref := v1alpha1.StateStoreReference{
				APIVersion: "platform.syntasso.io/v1alpha1",
				Kind:       "GitStateStore",
				Name:       "store",
			}
			Expect(ref.IsManagedByCustomController()).To(BeTrue())
		})
	})
})
