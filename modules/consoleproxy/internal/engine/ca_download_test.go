package engine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

func testRootPEM(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert
}

func serveCA(t *testing.T, dir, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	h := &caDownloadHandler{Dir: dir}
	rec := httptest.NewRecorder()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusTeapot)
		return nil
	})
	if err := h.ServeHTTP(rec, httptest.NewRequest(method, path, nil), next); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestCADownloadServesPublishedRootAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	crt, cert := testRootPEM(t)
	if err := os.WriteFile(filepath.Join(dir, "root.crt"), crt, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.sha256"), []byte(fingerprint(cert)), 0644); err != nil {
		t.Fatal(err)
	}

	rec := serveCA(t, dir, http.MethodGet, caDownloadCertPath)
	if rec.Code != http.StatusOK || rec.Body.String() != string(crt) {
		t.Fatalf("root.crt: status=%d body mismatch", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-x509-ca-cert" {
		t.Fatalf("content type = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download headers missing: %v", rec.Header())
	}

	rec = serveCA(t, dir, http.MethodGet, caDownloadFingerprintPath)
	if rec.Code != http.StatusOK || rec.Body.String() != fingerprint(cert) {
		t.Fatalf("root.sha256: status=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = serveCA(t, dir, http.MethodHead, caDownloadCertPath)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: status=%d bodyLen=%d", rec.Code, rec.Body.Len())
	}
	if rec = serveCA(t, dir, http.MethodPost, caDownloadCertPath); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rec.Code)
	}
	if rec = serveCA(t, dir, http.MethodGet, "/ca/root.key"); rec.Code != http.StatusTeapot {
		t.Fatalf("unknown path must fall through to next handler, got %d", rec.Code)
	}
}

func TestCADownloadRefusesMissingOrNonCertificateFiles(t *testing.T) {
	dir := t.TempDir()
	if rec := serveCA(t, dir, http.MethodGet, caDownloadCertPath); rec.Code != http.StatusNotFound {
		t.Fatalf("missing file status = %d", rec.Code)
	}
	crt, _ := testRootPEM(t)
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("secret")})
	for name, body := range map[string][]byte{
		"private key":           key,
		"key before cert":       append(append([]byte{}, key...), crt...),
		"cert then key":         append(append([]byte{}, crt...), key...),
		"two certs":             append(append([]byte{}, crt...), crt...),
		"text before cert":      append([]byte("note\n"), crt...),
		"not pem":               []byte("hello"),
		"empty":                 nil,
		"cert with extra bytes": append(append([]byte{}, crt...), []byte("junk")...),
	} {
		if err := os.WriteFile(filepath.Join(dir, "root.crt"), body, 0644); err != nil {
			t.Fatal(err)
		}
		rec := serveCA(t, dir, http.MethodGet, caDownloadCertPath)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s: status=%d, must not be served", name, rec.Code)
		}
	}
}

func TestRenderExposesCADownloadOnlyInInternalMode(t *testing.T) {
	c := config.Defaults()
	for mode, want := range map[string]bool{"internal": true, "public": false} {
		c.TLS.Mode = mode
		raw, err := Render(c)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(string(raw), "moox_console_ca") && strings.Contains(string(raw), caDownloadCertPath)
		if got != want {
			t.Fatalf("%s mode: CA download route present = %v, want %v", mode, got, want)
		}
	}
}
