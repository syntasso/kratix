package git //nolint:testpackage // Asserting the insecure flag needs access to private client state.

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/internal/ptr"
	corev1 "k8s.io/api/core/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("NewGitClient", func() {
	DescribeTable("carries the insecure flag through to the client",
		func(insecure bool) {
			client, err := NewGitClient(GitClientRequest{
				RawRepoURL: "https://github.com/syntasso/kratix",
				Root:       "state-store-path",
				Auth:       &Auth{Creds: NopCreds{}},
				Insecure:   insecure,
				Log:        logr.Discard(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(client.insecure).To(Equal(insecure))
		},
		Entry("when verification is disabled", true),
		Entry("when verification is enabled", false),
	)
})

var _ = Describe("SetAuth", func() {
	DescribeTable("carries the insecure flag through to basic auth creds",
		func(insecure bool) {
			auth, err := SetAuth(v1alpha1.GitStateStoreSpec{
				URL:        "https://github.com/syntasso/kratix",
				AuthMethod: v1alpha1.BasicAuthMethod,
				Insecure:   ptr.To(insecure),
				StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
					SecretRef: &corev1.SecretReference{Name: "creds", Namespace: "default"},
				},
			}, map[string][]byte{
				"username": []byte("user1"),
				"password": []byte("pw1"),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth.Creds.(HTTPSCreds).insecure).To(Equal(insecure))
		},
		Entry("when verification is disabled", true),
		Entry("when verification is enabled", false),
	)

	Describe("githubApp", func() {
		var (
			spec                           v1alpha1.GitStateStoreSpec
			creds                          map[string][]byte
			origGenerateGitHubAppJWT       func(string, string) (string, error)
			origGetGitHubInstallationToken func(string, string, string, bool) (string, error)
		)

		BeforeEach(func() {
			origGenerateGitHubAppJWT = GenerateGitHubAppJWT
			origGetGitHubInstallationToken = GetGitHubInstallationToken
			GenerateGitHubAppJWT = func(string, string) (string, error) { return "jwt", nil }
			GetGitHubInstallationToken = func(string, string, string, bool) (string, error) { return "token", nil }

			spec = v1alpha1.GitStateStoreSpec{
				URL:        "https://github.example.com/syntasso/kratix",
				AuthMethod: v1alpha1.GitHubAppAuthMethod,
				StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
					SecretRef: &corev1.SecretReference{Name: "creds", Namespace: "default"},
				},
			}
			creds = map[string][]byte{
				"appID":          []byte("123"),
				"installationID": []byte("456"),
				"privateKey":     []byte("dummy"),
			}
		})

		AfterEach(func() {
			GenerateGitHubAppJWT = origGenerateGitHubAppJWT
			GetGitHubInstallationToken = origGetGitHubInstallationToken
		})

		It("uses github.com by default", func() {
			auth, err := SetAuth(spec, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(auth.Creds.(GitHubAppCreds).baseURL).To(Equal("https://api.github.com"))
		})

		It("uses the GitHub API URL from the option", func() {
			auth, err := SetAuth(spec, creds, WithGitHubAPIURL("https://github.example.com/api/v3"))
			Expect(err).NotTo(HaveOccurred())
			Expect(auth.Creds.(GitHubAppCreds).baseURL).To(Equal("https://github.example.com/api/v3"))
		})
	})
})
