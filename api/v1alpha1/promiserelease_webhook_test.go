package v1alpha1_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/api/v1alpha1/v1alpha1fakes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("PromiseReleaseWebhook", func() {
	var (
		pr             *v1alpha1.PromiseRelease
		promiseFetcher v1alpha1fakes.FakePromiseFetcher
	)
	BeforeEach(func() {
		promiseFetcher = v1alpha1fakes.FakePromiseFetcher{}
		v1alpha1.SetPromiseFetcher(&promiseFetcher)

		pr = &v1alpha1.PromiseRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "mycrds.group.example",
				Namespace: "default",
			},
			Spec: v1alpha1.PromiseReleaseSpec{
				Version: "v0.1.0",
				SourceRef: v1alpha1.SourceRef{
					Type: "http",
					URL:  "example.com",
				},
			},
		}
	})

	When("source ref is unknown", func() {
		It("errors on create and update", func() {
			pr.Spec.SourceRef.Type = "ssh"
			warnings, err := pr.ValidateCreate()
			Expect(warnings).To(BeEmpty())
			Expect(err).To(MatchError("unknown sourceRef type \"ssh\""))

			warnings, err = pr.ValidateUpdate(pr)
			Expect(warnings).To(BeEmpty())
			Expect(err).To(MatchError("unknown sourceRef type \"ssh\""))
		})
	})

	When("URL is empty", func() {
		It("errors on create and update", func() {
			pr.Spec.SourceRef.URL = ""
			warnings, err := pr.ValidateCreate()
			Expect(warnings).To(BeEmpty())
			Expect(err).To(MatchError("sourceRef.url must be set"))

			warnings, err = pr.ValidateUpdate(pr)
			Expect(warnings).To(BeEmpty())
			Expect(err).To(MatchError("sourceRef.url must be set"))
		})
	})

	When("fetching the URL fails", func() {
		It("errors on create", func() {
			promiseFetcher.FromURLReturns(nil, fmt.Errorf("foo"))
			warnings, err := pr.ValidateCreate()
			Expect(warnings).To(BeEmpty())
			Expect(err).To(MatchError("failed to fetch promise: foo"))

			warnings, err = pr.ValidateUpdate(pr)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})

		It("does not error on update", func() {
			//We only want to fetch it on create, its expensive to do this call
			//frequently.
			promiseFetcher.FromURLReturns(nil, fmt.Errorf("foo"))
			warnings, err := pr.ValidateUpdate(pr)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
