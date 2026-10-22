package git //nolint:testpackage // APITLSConfig reads unexported certificate lookup helpers.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("APITLSConfig", func() {
	var (
		server  *httptest.Server
		host    string
		certPEM []byte
	)

	BeforeEach(func() {
		certPEM, server = tlsServerForLoopback()
		host = serverNameWithoutPort(server.Listener.Addr().String())
	})

	AfterEach(func() {
		server.Close()
	})

	get := func(config *tls.Config) error {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}}
		response, err := client.Get(server.URL)
		if err == nil {
			defer response.Body.Close()
		}
		return err
	}

	When("the host's certificate authority is under KRATIX_TLS_DATA_PATH", func() {
		BeforeEach(func() {
			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, host), certPEM, 0o600)).To(Succeed())
			GinkgoT().Setenv("KRATIX_TLS_DATA_PATH", dir)
		})

		It("trusts it", func() {
			Expect(get(APITLSConfig(server.Listener.Addr().String(), false))).To(Succeed())
		})
	})

	When("there is no certificate for the host", func() {
		BeforeEach(func() {
			GinkgoT().Setenv("KRATIX_TLS_DATA_PATH", GinkgoT().TempDir())
		})

		It("does not trust it, so a private authority is never silently accepted", func() {
			Expect(get(APITLSConfig(server.Listener.Addr().String(), false))).
				To(MatchError(ContainSubstring("certificate")))
		})

		It("trusts it anyway when the state store asked for insecure", func() {
			Expect(get(APITLSConfig(server.Listener.Addr().String(), true))).To(Succeed())
		})
	})
})

// tlsServerForLoopback returns a self-signed authority in PEM and a server using it.
func tlsServerForLoopback() ([]byte, *httptest.Server) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kratix-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalECPrivateKey(key)
	Expect(err).NotTo(HaveOccurred())

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	Expect(err).NotTo(HaveOccurred())

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()

	return certPEM, server
}
