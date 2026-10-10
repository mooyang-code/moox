package probe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/stretchr/testify/require"
)

func newProbeTLS(t *testing.T, handler http.Handler) (*httptest.Server, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	require.NoError(t, err)
	root, err = x509.ParseCertificate(der)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"console.example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	directory := t.TempDir()
	caFile, baseline := filepath.Join(directory, "root.crt"), filepath.Join(directory, "root.sha256")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	sum := sha256.Sum256(der)
	require.NoError(t, os.WriteFile(baseline, []byte(hex.EncodeToString(sum[:])), 0600))
	return srv, caFile, baseline
}

func TestHTTPSProbeConnectsLoopbackWithPublicHostAndSNI(t *testing.T) {
	for _, response := range []int{http.StatusOK, http.StatusFound} {
		t.Run(http.StatusText(response), func(t *testing.T) {
			srv, root, baseline := newProbeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "console.example.test:9527" || r.TLS.ServerName != "console.example.test" {
					t.Error("public Host/SNI was replaced with the connection address")
				}
				if r.Header.Get("X-Moox-Health-Auth") != "" {
					t.Error("page probe exposed health credentials")
				}
				w.Header().Set("Location", "https://redirect-must-not-be-followed.invalid/")
				w.WriteHeader(response)
			}))
			check := domain.Check{URL: "https://console.example.test:9527/readyz", ConnectAddress: srv.Listener.Addr().String(), ServerName: "console.example.test", TrustMode: "internal", CAFile: root, CABaseline: baseline, Source: domain.CheckSourcePlacement, ExpectedStatus: "200-399"}
			runner := HTTPRunner{HealthSigner: &HealthSigner{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "test-health-secret"}}
			result := runner.Run(t.Context(), check)
			require.True(t, result.Success, result.RawError)
			require.Equal(t, response, result.HTTPStatus)
		})
	}
}

func TestHTTPSProbeRejectsWrongCANameAndBaseline(t *testing.T) {
	srv, root, baseline := newProbeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	_, otherRoot, otherBaseline := newProbeTLS(t, http.NotFoundHandler())
	check := domain.Check{URL: "https://console.example.test:9527/", ConnectAddress: srv.Listener.Addr().String(), ServerName: "console.example.test", TrustMode: "internal", CAFile: root, CABaseline: baseline}
	for _, test := range []struct {
		name   string
		change func(*domain.Check)
	}{
		{"wrong_CA", func(c *domain.Check) { c.CAFile, c.CABaseline = otherRoot, otherBaseline }},
		{"wrong_name", func(c *domain.Check) { c.URL, c.ServerName = "https://other.example.test/", "other.example.test" }},
		{"wrong_baseline", func(c *domain.Check) { c.CABaseline = otherBaseline }},
		{"missing_baseline", func(c *domain.Check) { c.CABaseline = filepath.Join(t.TempDir(), "missing") }},
		{"Host_SNI_disagree", func(c *domain.Check) { c.ServerName = "other.example.test" }},
		{"public_uses_system_trust", func(c *domain.Check) { c.TrustMode, c.CAFile, c.CABaseline = "public", "", "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := check
			test.change(&bad)
			result := (HTTPRunner{Client: srv.Client()}).Run(t.Context(), bad)
			require.False(t, result.Success, "invalid trust was accepted")
			require.NotEmpty(t, result.RawError)
		})
	}
	public := check
	public.TrustMode, public.CAFile, public.CABaseline = "public", "", ""
	client, closeClient, err := httpsClient(public, time.Second)
	require.NoError(t, err)
	defer closeClient()
	require.Nil(t, client.Transport.(*http.Transport).TLSClientConfig.RootCAs, "public TLS must use the operating system trust store")
}

func TestPageDependencyFailureDoesNotChangeProxyReadiness(t *testing.T) {
	auth, err := healthz.NewAuthenticator(healthz.AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "test-health-secret"})
	require.NoError(t, err)
	ready := httptest.NewServer(auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ready":true}`)) })))
	defer ready.Close()
	page, root, baseline := newProbeTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("web-host is unavailable"))
	}))
	runner := HTTPRunner{HealthSigner: &HealthSigner{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "test-health-secret"}}
	pageResult := runner.Run(t.Context(), domain.Check{CheckID: "console-page:control:console-proxy", Source: domain.CheckSourcePlacement, URL: "https://console.example.test:9527/", ConnectAddress: page.Listener.Addr().String(), ServerName: "console.example.test", TrustMode: "internal", CAFile: root, CABaseline: baseline, ExpectedStatus: "200-399"})
	require.False(t, pageResult.Success)
	require.Contains(t, pageResult.RawError, "web-host is unavailable")
	proxyResult := runner.Run(t.Context(), domain.Check{CheckID: "placement:control:console-proxy", Source: domain.CheckSourcePlacement, URL: ready.URL + "/readyz", BodyContains: `"ready":true`})
	require.True(t, proxyResult.Success, proxyResult.RawError)
}
