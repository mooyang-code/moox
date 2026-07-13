package servicegateway

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientAllowsLoopbackHTTP(t *testing.T) {
	t.Setenv(CAFileEnv, "")
	t.Setenv(CAPEMBase64Env, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	client, err := NewClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestClientRejectsRemoteHTTP(t *testing.T) {
	client, err := NewClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get("http://example.com/api/service/test/Test")
	if err == nil || !strings.Contains(err.Error(), "non-loopback HTTP") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientTrustsPrivateCAFromFileAndBase64(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	pem := certificatePEM(t, srv)
	file := t.TempDir() + "/root.pem"
	if err := os.WriteFile(file, pem, 0600); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(){
		"file":   func() { t.Setenv(CAFileEnv, file); t.Setenv(CAPEMBase64Env, "") },
		"base64": func() { t.Setenv(CAFileEnv, ""); t.Setenv(CAPEMBase64Env, base64.StdEncoding.EncodeToString(pem)) },
	} {
		t.Run(name, func(t *testing.T) {
			setup()
			client, err := NewClient(time.Second)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}

func TestClientKeepsTLSHostnameAndTrustVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	pem := certificatePEM(t, srv)
	t.Setenv(CAPEMBase64Env, base64.StdEncoding.EncodeToString(pem))
	client, err := NewClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)); err == nil {
		t.Fatal("SAN mismatch accepted")
	}
	t.Setenv(CAPEMBase64Env, "")
	client, err = NewClient(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(srv.URL); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}

func certificatePEM(t *testing.T, srv *httptest.Server) []byte {
	t.Helper()
	return []byte("-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(srv.Certificate().Raw) + "\n-----END CERTIFICATE-----\n")
}
