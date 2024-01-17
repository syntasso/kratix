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
		p              *v1alpha1.Promise
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

		p = &v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foo",
				Labels: map[string]string{
					"kratix.io/promise-version": "v0.1.0",
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
			promiseFetcher.FromURLReturns(p, fmt.Errorf("foo"))
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
			promiseFetcher.FromURLReturns(p, fmt.Errorf("foo"))
			warnings, err := pr.ValidateUpdate(pr)
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})
	})

	When("the promise is missing the label", func() {
		It("emits a warning", func() {
			p.Labels = map[string]string{}
			promiseFetcher.FromURLReturns(p, nil)
			warnings, err := pr.ValidateCreate()
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf("Warning: version label (kratix.io/promise-version) not found on promise, installation will fail"))
		})
	})

	When("the promise is at a different version", func() {
		It("emits a warning", func() {
			p.Labels = map[string]string{
				"kratix.io/promise-version": "v0.2.0",
			}
			promiseFetcher.FromURLReturns(p, nil)
			warnings, err := pr.ValidateCreate()
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf("Warning: version labels do not match, found: v0.2.0, expected: v0.1.0, installation will fail"))
		})
	})
})
