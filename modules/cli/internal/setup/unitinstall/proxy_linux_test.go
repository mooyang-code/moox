//go:build linux

package unitinstall

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUnitActivationLinuxActualProxyDrainsAndPreservesCA(t *testing.T) {
	options := controlActivationOptions(t)
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	var finishOnce sync.Once
	releaseRequest := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(releaseRequest)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/slow" {
			started <- struct{}{}
			<-finish
		}
		_, _ = io.WriteString(w, "actual-response")
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	config := "public:\n  host: localhost\n  bind: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n  http3: false\ntls:\n  mode: internal\n  storage_root: ../data/caddy/caddy\n  ca_baseline: ../data/caddy/internal-ca.sha256\n  ca_publish_dir: ../certs/caddy\n  initialize_ca: true\nupstreams:\n  admin: " + upstream.Listener.Addr().String() + "\n  web: 127.0.0.1:9528\nhealth:\n  listen: 127.0.0.1:19528\nlifecycle:\n  drain_timeout: 3s\n  engine_stop_timeout: 1s\n  cleanup_margin: 1s\n  startup_timeout: 10s\n"
	override := filepath.Join(privateParent(t), "proxy.yaml")
	require.NoError(t, os.WriteFile(override, []byte(config), 0o600))
	options.Components = []string{"console-proxy", "web-host"}
	options.Environment["web-host"]["MOOX_WEB_HOST_ADDR"] = "127.0.0.1:9528"
	options.Environment["console-proxy"] = healthEnvironment()
	options.Overrides = map[string]string{"console-proxy/config/app.yaml": override}
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	require.Equal(t, []string{"web-host", "console-proxy"}, first.Components)
	cleanupActivation(t, first)
	_, err = activationCommand(t, "activate", "--directory", first.Directory)
	require.NoError(t, err)
	oldCA, err := proxyState(t.Context(), first)
	require.NoError(t, err)
	require.NotEmpty(t, oldCA)
	root, err := os.ReadFile(filepath.Join(first.Directory, "console-proxy/certs/caddy/root.crt"))
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(root))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	options.ReleaseID = "unsafe-initialize"
	unsafe, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", unsafe.Directory)
	require.Error(t, err)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	config = strings.ReplaceAll(config, "initialize_ca: true", "initialize_ca: false")
	config = strings.ReplaceAll(config, "drain_timeout: 3s", "drain_timeout: 100ms")
	config = strings.ReplaceAll(config, "engine_stop_timeout: 1s", "engine_stop_timeout: 100ms")
	config = strings.ReplaceAll(config, "cleanup_margin: 1s", "cleanup_margin: 100ms")
	require.NoError(t, os.WriteFile(override, []byte(config), 0o600))
	options.ReleaseID = "second"
	next, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	response := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1:"+strconv.Itoa(port)+"/api/admin/slow", nil)
		if err != nil {
			response <- err
			return
		}
		request.Host = "localhost"
		resp, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			err = readErr
			if err == nil && (resp.StatusCode != http.StatusOK || string(body) != "actual-response") {
				err = fmt.Errorf("long request returned unexpected status/body: %d", resp.StatusCode)
			}
		}
		response <- err
	}()
	select {
	case <-started:
	case err := <-response:
		t.Fatalf("long request ended before reaching the upstream: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("long request did not reach the running proxy")
	}
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(options.DeploymentRoot, "run/draining/console-proxy")); err == nil {
				time.Sleep(1500 * time.Millisecond)
				releaseRequest()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		releaseRequest()
	}()
	before := time.Now()
	_, err = activationCommand(t, "activate", "--directory", next.Directory)
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(before), 1500*time.Millisecond, "new shorter budget cannot truncate the old running request")
	require.NoError(t, <-response)
	nextCA, err := proxyState(t.Context(), next)
	require.NoError(t, err)
	require.Equal(t, oldCA, nextCA)
	_, err = activationCommand(t, "rollback", "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	rolledCA, err := proxyState(t.Context(), first)
	require.NoError(t, err)
	require.Equal(t, oldCA, rolledCA)
}
