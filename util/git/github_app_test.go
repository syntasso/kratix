package git_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/util/git"
)

var errBoom = errors.New("boom")

var _ = Describe("GenerateGitHubAppJWT", func() {
	It("returns a signed JWT for a PKCS#1 RSA private key", func() {
		pemStr := generatePEMFromPKCS1RSAKey()
		jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
		Expect(err).NotTo(HaveOccurred())
		Expect(jwt).NotTo(BeEmpty())
	})

	It("returns a signed JWT for a PKCS#8 RSA private key", func() {
		pemStr := generatePEMFromPKCS8RSAKey()
		jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
		Expect(err).NotTo(HaveOccurred())
		Expect(jwt).NotTo(BeEmpty())
	})

	It("returns an error for invalid PEM input", func() {
		jwt, err := git.GenerateGitHubAppJWT("12345", "not-a-pem")
		Expect(err).To(HaveOccurred())
		Expect(jwt).To(BeEmpty())
	})

	It("returns an error for a non-RSA PEM block", func() {
		block := pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("bad")}
		pemBytes := pem.EncodeToMemory(&block)
		jwt, err := git.GenerateGitHubAppJWT("12345", string(pemBytes))
		Expect(err).To(HaveOccurred())
		Expect(jwt).To(BeEmpty())
	})

	It("returns an error for a non-RSA PKCS#8 key", func() {
		pemStr := generatePEMFromPKCS8ECKey()
		jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
		Expect(err).To(HaveOccurred())
		Expect(jwt).To(BeEmpty())
	})
})

var _ = Describe("GetGitHubInstallationToken", func() {
	var server *httptest.Server

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
	})

	It("returns the token on a 201 response", func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer jwt"))
			Expect(r.Header.Get("Accept")).To(Equal("application/vnd.github+json"))

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "abc123"})
		}))

		tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
		Expect(err).NotTo(HaveOccurred())
		Expect(tok).To(Equal("abc123"))
	})

	It("returns an error on a non-201 response", func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "bad creds"})
		}))

		tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
		Expect(err).To(HaveOccurred())
		Expect(tok).To(BeEmpty())
		Expect(err.Error()).To(ContainSubstring("bad creds"))
	})

	It("returns an error when the token field is empty", func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": ""})
		}))

		tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
		Expect(err).To(HaveOccurred())
		Expect(tok).To(BeEmpty())
	})
})

func generatePEMFromPKCS1RSAKey() string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privDER := x509.MarshalPKCS1PrivateKey(key)
	block := pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}

func generatePEMFromPKCS8RSAKey() string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privDER, _ := x509.MarshalPKCS8PrivateKey(key)
	block := pem.Block{Type: "PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}

func generatePEMFromPKCS8ECKey() string {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	privDER, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	block := pem.Block{Type: "PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}
