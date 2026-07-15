package gatewayauth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func certificatePEM(server *httptest.Server) []byte {
	return []byte("-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(server.Certificate().Raw) + "\n-----END CERTIFICATE-----\n")
}
