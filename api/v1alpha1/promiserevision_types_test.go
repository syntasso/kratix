package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("PromiseRevision", func() {
	Describe("Constructor", func() {
		var promise *platformv1alpha1.Promise
		var promiseVersion string

		BeforeEach(func() {
			promise = &platformv1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{Name: "mypromise"},
				Spec: platformv1alpha1.PromiseSpec{
					API: &runtime.RawExtension{
						Raw: []byte(`{"apiVersion":"v1","kind":"Promise","metadata":{"name":"mypromise"}}`),
					},
				},
			}
			promiseVersion = "v1.0.0"
		})

		It("generates the correct set of matchLabels", func() {
			revision := platformv1alpha1.NewPromiseRevision(promise, promiseVersion)
			Expect(revision.Name).To(Equal("mypromise-2888c"))
			Expect(revision.Labels).To(HaveKeyWithValue("kratix.io/promise-name", "mypromise"))
			Expect(revision.Spec.PromiseRef.Name).To(Equal("mypromise"))
			Expect(revision.Spec.Version).To(Equal("v1.0.0"))
		})
	})
})
