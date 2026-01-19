package system_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/davecgh/go-spew/spew"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/util/git"
)

/*
NOTE: To run these tests configure the following environment variables:

TEST_GIT_WRITER_GITHUB_APP_ID
TEST_GIT_WRITER_GITHUB_APP_INSTALLATION_ID
# base64 encoded
TEST_GIT_WRITER_GITHUB_APP_PRIVATE_KEY
TEST_GIT_WRITER_GITHUB_HTTP_PAT
# base64 encoded
TEST_GIT_WRITER_GITHUB_SSH_PRIVATE_KEY

*/

var _ = Describe("Git tests", func() {

	//logger := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)).WithName("git-writer")
	//	httpPublicRepo := "https://github.com/syntasso/testing-git-writer-public.git"
	httpPrivateRepo := "https://github.com/syntasso/testing-git-writer-private.git"
	//sshPrivateRepo := "ssh://git@github.com/syntasso/testing-git-writer-private.git"

	Describe("Git native client tests", func() {

		When("targeting a private repository", func() {

			When("using HTTP basic auth", func() {

				appID := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_ID")
				fmt.Printf("SSSSSSSSSSSSSSSSSSSS: %v\n", AddSpaces(appID))

				fmt.Printf("Eeeeeeeeeeennnnnnn: %v\n", spew.Sdump(os.Environ()))

				fmt.Printf("TEST::::::::: %v - %v - %v\n",
					os.Getenv("TEST_GIT_WRITER_GITHUB_APP_ID"),
					os.Getenv("DOCKER_BUILDKIT"),
					os.Getenv("ACK_GINKGO_RC"))

				httpCreds := git.NewHTTPSCreds(
					"x-access-token",                        // username
					string(getGithubPATCreds()["password"]), // password
					"",                                      // bearer token
					"",                                      // clientCertData
					"",                                      // clientCertKey
					false,                                   // insecure
					git.NoopCredsStore{},                    // CredsStore,
					true,                                    // forceBasicAuth
				)

				It("successfully clones the repository", func() {
					client, err := git.NewGitClient(git.GitClientRequest{
						RawRepoURL: httpPrivateRepo,
						Root:       "",
						Auth:       &git.Auth{Creds: httpCreds},
						Insecure:   true,
						Proxy:      "",
						NoProxy:    "",
					})
					Expect(err).ToNot(HaveOccurred())

					repo, err := client.Init()
					Expect(err).ToNot(HaveOccurred())
					Expect(repo).ToNot(BeNil())

					err = client.Fetch("main", 0)
					Expect(err).ToNot(HaveOccurred())

					out, err := client.Checkout("main")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())
				})

			})
		})
	})
})

func getGithubAppCreds() map[string][]byte {
	envGithubAppAppID := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_ID")
	envGithubAppInstallationId := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_INSTALLATION_ID")
	return map[string][]byte{

		"appID":          []byte(envGithubAppAppID),
		"installationID": []byte(envGithubAppInstallationId),
		"privateKey":     getGithubAppPrivateKey(),
	}
}

func getGithubAppPrivateKey() []byte {
	envGithubAppPrivateKey := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_PRIVATE_KEY")
	githubAppPrivateKey, err := base64.StdEncoding.DecodeString(envGithubAppPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	return githubAppPrivateKey
}

func getGithubPATCreds() map[string][]byte {
	ghPat := os.Getenv("TEST_GIT_WRITER_GITHUB_HTTP_PAT")
	return map[string][]byte{
		"username": []byte("x-access-token"),
		"password": []byte(ghPat),
	}
}

func getGithubSSHCreds() map[string][]byte {
	envGithubSSHPrivateKey := os.Getenv("TEST_GIT_WRITER_GITHUB_SSH_PRIVATE_KEY")
	githubSSHPrivateKey, err := base64.StdEncoding.DecodeString(envGithubSSHPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	return map[string][]byte{
		"sshPrivateKey": githubSSHPrivateKey,
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}

func getTestDataToSave(content string) (v1alpha1.Workload, error) {
	resourcesDir := "resources"
	canaryConfigMapPath := "kratix-canary-configmap.yaml"
	if content == "" {
		content = fmt.Sprintf("this confirms your infrastructure is reading from Kratix state stores (%d)", rand.Int())
	}
	kratixConfigMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kratix-info",
			Namespace: "kratix-worker-system",
		},
		Data: map[string]string{
			"test": content,
		},
	}
	nsBytes, err := yaml.Marshal(kratixConfigMap)
	Expect(err).ToNot(HaveOccurred())

	resource := v1alpha1.Workload{
		Filepath: filepath.Join(resourcesDir, canaryConfigMapPath),
		Content:  string(nsBytes),
	}

	return resource, err
}

func getInvalidSSHCreds() map[string][]byte {
	key, err := rsa.GenerateKey(cryptorand.Reader, 1024)
	Expect(err).NotTo(HaveOccurred())

	privateKeyPEM := pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	var b bytes.Buffer
	if err := pem.Encode(&b, &privateKeyPEM); err != nil {
		log.Fatalf("Failed to write private key to buffer: %v", err)
	}

	return map[string][]byte{
		"sshPrivateKey": b.Bytes(),
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}

func getStateStoreAndDestBaseDir(store *v1alpha1.GitStateStoreSpec, dest *v1alpha1.Destination) string {
	return fmt.Sprintf("%s/%s", store.Path, dest.Spec.Path)
}

func getStateStoreAndDest(authType, repo string) (*v1alpha1.GitStateStoreSpec, *v1alpha1.Destination) {

	return &v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path: "state-store-path",
				SecretRef: &v1.SecretReference{
					Namespace: "default",
					Name:      "dummy-secret",
				},
			},
			AuthMethod: authType,
			URL:        repo,
			Branch:     "main",
			GitAuthor: v1alpha1.GitAuthor{
				Email: "test@example.com",
				Name:  "a-user",
			},
		}, &v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test",
			},
			Spec: v1alpha1.DestinationSpec{
				Path: fmt.Sprintf("%s-dst-path/", authType),
			},
		}
}

func AddSpaces(s string) string {
	return strings.Join(strings.Split(s, ""), " ")
}
