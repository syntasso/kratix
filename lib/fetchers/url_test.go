package fetchers_test

import (
	"net/http"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/fetchers"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("#FromURL", func() {
	var (
		responseBody   []byte
		responseStatus int
		fakeServer     *ghttp.Server
		urlFetcher     *fetchers.URLFetcher
	)

	BeforeEach(func() {
		fakeServer = ghttp.NewServer()
		responseStatus = http.StatusOK

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if responseStatus != http.StatusOK {
				w.WriteHeader(responseStatus)
				return
			}
			w.Write(responseBody)
		})

		fakeServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/"),
				handler,
			),
		)

		urlFetcher = &fetchers.URLFetcher{}
	})

	AfterEach(func() {
		fakeServer.Close()
	})

	When("the url points to a valid promise document", func() {
		BeforeEach(func() {
			var err error
			responseBody, err = os.ReadFile("assets/promise.yaml")
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns a Promise and no error", func() {
			promise, err := urlFetcher.FromURL(fakeServer.URL(), "")
			Expect(err).ToNot(HaveOccurred())

			expectedPromise := &v1alpha1.Promise{}
			err = yaml.Unmarshal(responseBody, expectedPromise)
			Expect(err).ToNot(HaveOccurred())

			Expect(promise).To(Equal(expectedPromise))
		})
	})

	When("the url requires authorization", func() {
		BeforeEach(func() {
			var err error
			responseBody, err = os.ReadFile("assets/promise.yaml")
			Expect(err).ToNot(HaveOccurred())
		})

		It("an authorization header can be provided", func() {
			_, err := urlFetcher.FromURL(fakeServer.URL(), "bearer abc123")
			Expect(err).ToNot(HaveOccurred())

			req := fakeServer.ReceivedRequests()
			Expect(req).To(HaveLen(1))
			Expect(req[0].Header.Get("Authorization")).To(Equal("bearer abc123"))
		})
	})

	When("the url is invalid", func() {
		It("errors", func() {
			_, err := urlFetcher.FromURL("invalid-url", "")
			Expect(err).To(MatchError(ContainSubstring("failed to get url")))
		})
	})

	When("the url points to a file containing multiple objects", func() {
		BeforeEach(func() {
			var err error
			responseBody, err = os.ReadFile("assets/promise-and-namespace.yaml")
			Expect(err).ToNot(HaveOccurred())
		})

		It("errors", func() {
			_, err := urlFetcher.FromURL(fakeServer.URL(), "")
			Expect(err).To(MatchError(ContainSubstring("expected single document yaml, but found multiple documents")))
		})
	})

	When("the url points to a file containing a non-Promise k8s object", func() {
		BeforeEach(func() {
			responseBody = []byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"test"}}`)
		})

		It("errors", func() {
			_, err := urlFetcher.FromURL(fakeServer.URL(), "")
			Expect(err).To(MatchError("expected single Promise object but found object of kind: Namespace"))
		})
	})

	When("the HTTP response contains an error code", func() {
		BeforeEach(func() {
			responseStatus = http.StatusBadRequest
		})

		It("errors", func() {
			_, err := urlFetcher.FromURL(fakeServer.URL(), "")
			Expect(err).To(MatchError("failed to get Promise from URL: status code 400"))
		})
	})

	When("the HTTP body is invalid YAML", func() {
		BeforeEach(func() {
			responseBody = []byte("invalid yaml")
		})

		It("errors", func() {
			_, err := urlFetcher.FromURL(fakeServer.URL(), "")
			Expect(err).To(MatchError(ContainSubstring("failed to unmarshal into Kubernetes object: ")))
		})
	})

	DescribeTable("invalid documents", func(response string) {
		responseBody = []byte(response)
		_, err := urlFetcher.FromURL(fakeServer.URL(), "")
		Expect(err).To(MatchError(ContainSubstring("failed to unmarshal into Kubernetes object: ")))
	},
		Entry("empty document", ""),
		Entry("invalid yaml", "invalid yaml"),
		Entry("valid yaml, but invalid k8s", "key: value"),
	)
})
