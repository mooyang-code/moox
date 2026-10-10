package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/engine"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/requestauth"
)

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func signedRequest(t *testing.T, address, path, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := requestauth.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	sig, err := requestauth.Sign(secret, requestauth.Material{Method: req.Method, Path: path, Timestamp: timestamp, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("moox-health-v1/test-monitor/%d/%s/%s", timestamp, nonce, sig))
	return req
}

func TestAuthenticatedDiagnosticsReadinessAndServeDrain(t *testing.T) {
	const secret = "console-proxy-test-health-secret"
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "test-monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", secret)
	entered := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("started\n"))
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-release:
			_, _ = w.Write([]byte("finished\n"))
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	cfg := config.Defaults()
	cfg.Public.Host, cfg.Public.Bind, cfg.Public.HTTP3 = "bootstrap-console.localhost", "127.0.0.1", false
	public := freeAddress(t)
	_, port, _ := net.SplitHostPort(public)
	if _, err := fmt.Sscanf(port, "%d", &cfg.Public.Port); err != nil {
		t.Fatal(err)
	}
	cfg.Health.Listen = freeAddress(t)
	cfg.Upstreams.Admin, cfg.Upstreams.Web = strings.TrimPrefix(upstream.URL, "http://"), freeAddress(t)
	dir := t.TempDir()
	cfg.TLS.StorageRoot = filepath.Join(dir, "data", "caddy", "caddy")
	cfg.TLS.CABaseline = filepath.Join(dir, "data", "caddy", "internal-ca.sha256")
	cfg.TLS.CAPublishDir = filepath.Join(dir, "certs", "caddy")
	if _, err := engine.InitializeState(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	cfg.Lifecycle.DrainTimeout, cfg.Lifecycle.EngineStopTimeout = 3*time.Second, 2*time.Second
	cfg.Lifecycle.CleanupMargin, cfg.Lifecycle.StartupTimeout = time.Second, 5*time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, "test-version", "test-commit") }()
	// This cleanup also runs if an assertion fails during an active request.
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(cfg.StopBudget() + time.Second):
			t.Error("Serve failed to exit within its stop budget")
		}
	})
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	waitReady := func(want int) healthz.Response {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			rsp, err := client.Do(signedRequest(t, cfg.Health.Listen, "/readyz", secret))
			if err == nil {
				var payload healthz.Response
				decodeErr := json.NewDecoder(rsp.Body).Decode(&payload)
				rsp.Body.Close()
				if decodeErr == nil && rsp.StatusCode == want {
					return payload
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("readyz did not reach %d", want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	payload := waitReady(http.StatusOK)
	if payload.Module != "console-proxy" || payload.Version != "test-version" || payload.GitCommit != "test-commit" {
		t.Fatalf("unexpected component identity: %+v", payload)
	}
	dependencies := payload.Details["upstream_tcp_reachable"].(map[string]any)
	if dependencies["web-host"] != false || !payload.Ready {
		t.Fatal("web-host failure incorrectly made the proxy unready")
	}
	for _, path := range []string{"/readyz", "/healthz", "/metrics"} {
		rsp, err := client.Get("http://" + cfg.Health.Listen + path)
		if err != nil {
			t.Fatal(err)
		}
		rsp.Body.Close()
		if rsp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous %s returned %d", path, rsp.StatusCode)
		}
	}
	request := signedRequest(t, cfg.Health.Listen, "/readyz", secret)
	for attempt, want := range []int{http.StatusOK, http.StatusUnauthorized} {
		rsp, err := client.Do(request.Clone(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		rsp.Body.Close()
		if rsp.StatusCode != want {
			t.Fatalf("signed request attempt %d returned %d", attempt, rsp.StatusCode)
		}
	}
	rsp, err := client.Do(signedRequest(t, cfg.Health.Listen, "/healthz", "wrong-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()
	if rsp.StatusCode != http.StatusUnauthorized {
		t.Fatal("wrong health HMAC secret was accepted")
	}
	root, err := os.ReadFile(filepath.Join(cfg.TLS.CAPublishDir, "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root) {
		t.Fatal("invalid published CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: cfg.Public.Host, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	proxyClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, _ = http.NewRequest(http.MethodGet, "https://"+public+"/api/admin/stream", nil)
	request.Host = cfg.Public.Host
	rsp, err = proxyClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()
	<-entered
	cancel()
	if payload = waitReady(http.StatusServiceUnavailable); payload.Ready {
		t.Fatal("draining proxy remained ready")
	}
	alive, err := client.Do(signedRequest(t, cfg.Health.Listen, "/healthz", secret))
	if err != nil {
		t.Fatal(err)
	}
	alive.Body.Close()
	if alive.StatusCode != http.StatusOK {
		t.Fatal("draining process should remain live")
	}
	close(release)
	body, err := io.ReadAll(rsp.Body)
	if err != nil || string(body) != "started\nfinished\n" {
		t.Fatalf("Serve cancellation truncated an admitted stream: %q %v", body, err)
	}
}

func TestServeRequiresHealthCredentialsBeforeListening(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "")
	cfg := config.Defaults()
	cfg.Health.Listen = freeAddress(t)
	if err := Serve(context.Background(), cfg, "test", "test"); err == nil {
		t.Fatal("missing health credentials were accepted")
	}
	l, err := net.Listen("tcp", cfg.Health.Listen)
	if err != nil {
		t.Fatal("authentication failure leaked diagnostic listener")
	}
	l.Close()
}
