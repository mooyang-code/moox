package engine

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

func port(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testConfig(t *testing.T, admin, web string) config.Config {
	t.Helper()
	c := config.Defaults()
	// Caddy's process certificate cache survives library Stop. Production
	// changes use a new process; distinct test roots must use distinct names.
	name := sha256.Sum256([]byte(t.Name()))
	c.Public.Host = fmt.Sprintf("console-%x.localhost", name[:6])
	c.Public.Bind, c.Public.Port, c.Public.HTTP3 = "127.0.0.1", port(t), false
	c.Health.Listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(port(t)))
	dir := t.TempDir()
	c.TLS.StorageRoot, c.TLS.CABaseline, c.TLS.CAPublishDir = filepath.Join(dir, "data", "caddy", "caddy"), filepath.Join(dir, "data", "caddy", "internal-ca.sha256"), filepath.Join(dir, "certs", "caddy")
	c.Upstreams.Admin, c.Upstreams.Web = strings.TrimPrefix(admin, "http://"), strings.TrimPrefix(web, "http://")
	c.Lifecycle.StartupTimeout = 10 * time.Second
	c.Lifecycle.DrainTimeout = 250 * time.Millisecond
	c.Lifecycle.EngineStopTimeout = 2 * time.Second
	c.Lifecycle.CleanupMargin = time.Second
	return c
}

func start(t *testing.T, c config.Config) (*Engine, *http.Client, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(caDir(c), "root.crt")); os.IsNotExist(err) {
		if _, err := InitializeState(context.Background(), c, false); err != nil {
			t.Fatal(err)
		}
	}
	e, err := Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	root, err := os.ReadFile(filepath.Join(c.TLS.CAPublishDir, "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root) {
		t.Fatal("invalid CA export")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Public.Port)))
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return e, &http.Client{Transport: transport, Timeout: 5 * time.Second}, "https://" + c.Public.Host + ":" + strconv.Itoa(c.Public.Port)
}

func TestRenderedConfigurationInvariants(t *testing.T) {
	c := config.Defaults()
	for _, mode := range []string{"internal", "public"} {
		c.TLS.Mode = mode
		raw, err := Render(c)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		admin := parsed["admin"].(map[string]any)
		if admin["disabled"] != true || admin["config"].(map[string]any)["persist"] != false {
			t.Fatal("admin/persistence enabled")
		}
		apps := parsed["apps"].(map[string]any)
		if mode == "internal" {
			pki := apps["pki"].(map[string]any)
			ca := pki["certificate_authorities"].(map[string]any)["local"].(map[string]any)
			if ca["install_trust"] != false {
				t.Fatal("automatic trust installation enabled")
			}
		} else if _, ok := apps["pki"]; ok {
			t.Fatal("public mode would provision an unused internal CA")
		}
		if strings.Contains(string(raw), "11001") || strings.Contains(string(raw), "2019") {
			t.Fatal("legacy listener rendered")
		}
	}
}

func TestStateCheckDoesNotProvisionOrWriteLegacyBaseline(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ready")) }))
	defer upstream.Close()
	c := testConfig(t, upstream.URL, upstream.URL)
	if _, err := CheckState(c); err == nil {
		t.Fatal("accepted absent internal CA")
	}
	if _, err := os.Stat(c.TLS.StorageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only check created state")
	}
	e, _, _ := start(t, c)
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want, err := CheckState(c)
	if err != nil || len(want) != 95 || strings.TrimSpace(want) != want {
		t.Fatalf("persisted state failed: %v", err)
	}
	if err := os.Remove(c.TLS.CABaseline); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckState(c); err == nil {
		t.Fatal("serving-state check accepted a missing persistent baseline")
	}
	got, err := CheckImportState(c)
	if err != nil || got != want {
		t.Fatalf("trusted legacy state failed read-only check: %v", err)
	}
	if _, err := os.Stat(c.TLS.CABaseline); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only check wrote a legacy baseline")
	}
}

func TestHTTPSRoutesTrustAndCAContinuity(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.RequestURI() != "/api/admin/signed?q=a%2Fb" || r.Header.Get("X-Moox-Auth") != "signed-material" || string(body) != "exact-body" {
			t.Error("signed request material changed")
		}
		_, _ = w.Write([]byte("admin"))
	}))
	defer admin.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("page")) }))
	defer web.Close()
	c := testConfig(t, admin.URL, web.URL)
	e, client, base := start(t, c)
	for _, path := range []string{"/", "/assets/hash.js", "/dashboard"} {
		rsp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rsp.Body)
		rsp.Body.Close()
		if rsp.StatusCode != 200 || string(body) != "page" || rsp.Header.Get("X-Frame-Options") != "DENY" {
			t.Fatalf("page %s failed: %d %s", path, rsp.StatusCode, body)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/api/admin/signed?q=a%2Fb", strings.NewReader("exact-body"))
	req.Header.Set("X-Moox-Auth", "signed-material")
	rsp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rsp.Body)
	rsp.Body.Close()
	if string(body) != "admin" {
		t.Fatal("admin routing failed")
	}
	for _, path := range []string{"/api", "/api/unknown", "/api/service/x", "/api/gateway-control/routes", "/healthz", "/healthz/x", "/readyz/", "/metrics", "/api/admin/../service/x", "/api%2fservice/x", "/api//service/x"} {
		rsp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		rsp.Body.Close()
		if rsp.StatusCode != 404 {
			t.Errorf("forbidden path %q returned %d", path, rsp.StatusCode)
		}
	}
	wrongHost, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	wrongHost.Host = "unconfigured.example.com"
	rsp, err = client.Do(wrongHost)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()
	if rsp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured Host returned %d", rsp.StatusCode)
	}
	badTrustTransport := client.Transport.(*http.Transport).Clone()
	badTrustTransport.TLSClientConfig.RootCAs = x509.NewCertPool()
	defer badTrustTransport.CloseIdleConnections()
	badTrust := &http.Client{Timeout: time.Second, Transport: badTrustTransport}
	if rsp, err := badTrust.Get(base + "/"); err == nil {
		rsp.Body.Close()
		t.Error("wrong CA was accepted")
	} else {
		var verification *tls.CertificateVerificationError
		if !errors.As(err, &verification) {
			t.Fatalf("failed for a reason other than certificate verification: %v", err)
		}
	}
	wrongName := client.Transport.(*http.Transport).Clone()
	wrongName.TLSClientConfig.ServerName = "wrong.example.com"
	defer wrongName.CloseIdleConnections()
	if rsp, err := (&http.Client{Transport: wrongName, Timeout: time.Second}).Get(base + "/"); err == nil {
		rsp.Body.Close()
		t.Error("wrong SNI was accepted")
	}
	before, _ := os.ReadFile(c.TLS.CABaseline)
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reading lower-case colon-free fingerprints must not report a rotation.
	if err := os.WriteFile(c.TLS.CABaseline, []byte(strings.ToLower(strings.ReplaceAll(string(before), ":", ""))), 0600); err != nil {
		t.Fatal(err)
	}
	e2, err := Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(c.TLS.CABaseline)
	if string(before) != string(after) {
		t.Fatal("CA identity/format changed on redeploy")
	}
	if err := os.WriteFile(c.TLS.CABaseline, []byte(strings.Repeat("00:", 31)+"00\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), c); err == nil {
		t.Fatal("mismatched CA fingerprint accepted")
	}
	if err := os.WriteFile(c.TLS.CABaseline, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(caDir(c), "root.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), c); err == nil {
		t.Fatal("missing existing key accepted")
	}
}

func TestStreamingAndWebSocketDrain(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	upgrader := websocket.Upgrader{}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			for {
				kind, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if err := conn.WriteMessage(kind, msg); err != nil {
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("started\n"))
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte("finished\n"))
		case <-r.Context().Done():
		}
	}))
	defer admin.Close()
	c := testConfig(t, admin.URL, admin.URL)
	e, client, base := start(t, c)
	rsp, err := client.Get(base + "/api/admin/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- e.Stop(context.Background()) }()
	// Synchronize with the gate itself; no guessed sleeps to claim draining.
	deadline := time.Now().Add(time.Second)
	for {
		e.gate.mu.Lock()
		open := e.gate.open
		e.gate.mu.Unlock()
		if !open {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drain did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	rejected, err := client.Get(base + "/api/admin/new")
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != 503 {
		t.Fatal("new work admitted while draining")
	}
	close(release)
	body, err := io.ReadAll(rsp.Body)
	if err != nil || !strings.Contains(string(body), "finished") {
		t.Fatalf("stream did not drain: %s %v", body, err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	// The standard Caddy upgrade handler must retain admission until both
	// directions of the SSH-like WebSocket stream have finished.
	e, client, base = start(t, c)
	dialer := websocket.Dialer{TLSClientConfig: client.Transport.(*http.Transport).TLSClientConfig.Clone(), NetDialContext: client.Transport.(*http.Transport).DialContext, HandshakeTimeout: time.Second}
	conn, _, err := dialer.Dial("wss"+strings.TrimPrefix(base, "https")+"/api/admin/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("terminal")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != "terminal" {
		t.Fatalf("WS echo failed: %s %v", msg, err)
	}
	started := time.Now()
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < c.Lifecycle.DrainTimeout {
		t.Fatal("WebSocket lifetime was not counted")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("WebSocket remained open after forced drain")
	}
}

func TestFirstInstallRequiresExplicitStateDecision(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:11000", "http://127.0.0.1:9528")
	if _, err := Start(context.Background(), c); err == nil {
		t.Fatal("missing CA treated as first install")
	}
	if _, err := os.Stat(c.TLS.StorageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed preflight changed state")
	}
	if err := os.MkdirAll(caDir(c), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), c); err == nil {
		t.Fatal("partial CA state was regenerated")
	}
}

func TestPublicOnlyStateRemainsWithoutInternalCA(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:11000", "http://127.0.0.1:9528")
	c.TLS.Mode = "public"
	previous, err := prepareCA(c)
	if err != nil || previous != nil {
		t.Fatalf("public first install required an internal CA: %v", err)
	}
	if _, err := publishCA(c, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.TLS.StorageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("public-only state created an internal CA")
	}
	if err := os.MkdirAll(filepath.Dir(c.TLS.CABaseline), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.TLS.CABaseline, []byte("historical"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCA(c); err == nil {
		t.Fatal("public mode ignored a missing historical internal CA")
	}
}

func TestFailedStartupReleasesListenerAndOwnership(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:11000", "http://127.0.0.1:9528")
	if _, err := InitializeState(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", net.JoinHostPort(c.Public.Bind, strconv.Itoa(c.Public.Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if _, err := Start(context.Background(), c); err == nil {
		t.Fatal("startup succeeded despite port conflict")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	// A failed listener must release ownership without replacing the CA.
	e, err := Start(context.Background(), c)
	if err != nil {
		t.Fatalf("failed startup leaked engine ownership: %v", err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(c.Public.Bind, strconv.Itoa(c.Public.Port)))
	if err != nil {
		t.Fatalf("stopped engine retained TCP listener: %v", err)
	}
	listener.Close()
}
