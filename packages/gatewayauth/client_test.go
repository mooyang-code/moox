package gatewayauth

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type idleConnectionTrackingTransport struct{ closed int }

func (t *idleConnectionTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (t *idleConnectionTrackingTransport) CloseIdleConnections() { t.closed++ }

func TestHTTPClientRejectsNonLoopbackPlaintext(t *testing.T) {
	client, err := NewHTTPClient(ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get("http://example.com/v1/run")
	if err == nil || !strings.Contains(err.Error(), "non-loopback HTTP") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPClientAllowsLoopbackHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewHTTPClient(ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestHTTPClientAllowsHTTPSAndAppendsCAFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(caFile, certificatePEM(server), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient(ClientOptions{Timeout: time.Second, CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestHTTPClientRejectsMissingAndInvalidCAFiles(t *testing.T) {
	if _, err := NewHTTPClient(ClientOptions{CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("missing CA file accepted")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPClient(ClientOptions{CAFile: invalid}); err == nil {
		t.Fatal("invalid CA file accepted")
	}
}

func TestHTTPClientAllowsHTTPSWithBase64CAPEM(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	client, err := NewHTTPClient(ClientOptions{Timeout: time.Second, CAPEMBase64: base64.StdEncoding.EncodeToString(certificatePEM(server))})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestHTTPClientRejectsConflictingOrInvalidCAMaterial(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(caFile, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPClient(ClientOptions{CAFile: caFile, CAPEMBase64: "bm90LWEtY2VydA=="}); err == nil {
		t.Fatal("conflicting CA inputs accepted")
	}
	if _, err := NewHTTPClient(ClientOptions{CAPEMBase64: "%%%"}); err == nil {
		t.Fatal("invalid base64 CA accepted")
	}
	if _, err := NewHTTPClient(ClientOptions{CAPEMBase64: base64.StdEncoding.EncodeToString([]byte("not a certificate"))}); err == nil {
		t.Fatal("invalid PEM CA accepted")
	}
}

func TestHTTPClientRejectsRedirectWithoutContactingTarget(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := NewHTTPClient(ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(redirect.URL); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target contacted %d times", targetCalls)
	}
}

func TestCloseIdleConnectionsForwardsThroughSecureTransport(t *testing.T) {
	transport := &idleConnectionTrackingTransport{}
	client := &http.Client{Transport: secureRoundTripper{next: transport}}

	CloseIdleConnections(client)

	if transport.closed != 1 {
		t.Fatalf("closed idle connections = %d, want 1", transport.closed)
	}
}

func certificatePEM(server *httptest.Server) []byte {
	return []byte("-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(server.Certificate().Raw) + "\n-----END CERTIFICATE-----\n")
}
