//go:build linux

package unitruntime

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The gate supplies the real pure-Go proxy executable built on the operator's
// machine. Tests never compile it implicitly on the Linux execution host.
func TestRuntimeLinuxRealProxyDrainsHTTPSAndPreservesCAWhilePaused(t *testing.T) {
	binary := os.Getenv("MOOX_RUNTIME_PROXY_BINARY")
	if binary == "" {
		t.Skip("gate must supply the prebuilt Linux console-proxy executable")
	}
	path, plan := planFixture(t, "console-proxy")
	values := fixtureEnvironment()
	delete(values, "MOOX_RUNTIME_TEST_CHILD")
	writeFixture(t, plan.Components[0].EnvironmentFile, values)
	require.NoError(t, os.MkdirAll(filepath.Join(plan.ReleaseRoot, "bin"), 0o700))
	input, err := os.Open(binary)
	require.NoError(t, err)
	output, err := os.OpenFile(filepath.Join(plan.ReleaseRoot, "bin", "moox-console-proxy"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, input.Close())
	require.NoError(t, output.Close())
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "admin") }))
	defer admin.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/long" {
			_, _ = io.WriteString(w, "started\n")
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Second)
			_, _ = io.WriteString(w, "completed\n")
			return
		}
		_, _ = io.WriteString(w, "web")
	}))
	defer web.Close()
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := portListener.Addr().(*net.TCPAddr).Port
	require.NoError(t, portListener.Close())
	config := map[string]any{
		"public": map[string]any{"host": "localhost", "bind": "127.0.0.1", "port": port, "http3": false},
		"tls": map[string]any{
			"mode": "internal", "storage_root": filepath.Join(plan.ReleaseRoot, "data", "caddy"),
			"ca_baseline": filepath.Join(plan.ReleaseRoot, "data", "internal-ca.sha256"), "ca_publish_dir": filepath.Join(plan.ReleaseRoot, "certs", "caddy"),
		},
		"upstreams": map[string]string{"admin": strings.TrimPrefix(admin.URL, "http://"), "web": strings.TrimPrefix(web.URL, "http://")},
		"health":    map[string]string{"listen": "127.0.0.1:19528"},
		"lifecycle": map[string]string{"drain_timeout": "3s", "engine_stop_timeout": "1s", "cleanup_margin": "1s", "startup_timeout": "10s"},
	}
	writeConfig := func() {
		raw, err := yaml.Marshal(config)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(plan.ReleaseRoot, "console-proxy", "config", "app.yaml"), raw, 0o600))
	}
	writeConfig()
	initialization, err := exec.CommandContext(t.Context(), binary, "initialize-state", "--config", filepath.Join(plan.ReleaseRoot, "console-proxy/config/app.yaml")).CombinedOutput()
	require.NoError(t, err, "%s", initialization)
	cleanupRuntime(t, path)
	started := runRuntime(t, path, "start")
	require.True(t, started.Components[0].Ready)
	rootPath := filepath.Join(plan.ReleaseRoot, "certs", "caddy", "root.crt")
	root, err := os.ReadFile(rootPath)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(root))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	var response *http.Response
	require.Eventually(t, func() bool {
		response, err = client.Get(fmt.Sprintf("https://localhost:%d/long", port))
		return err == nil
	}, 5*time.Second, 100*time.Millisecond)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "started\n", first)
	before := time.Now()
	paused := runRuntime(t, path, "pause")
	require.False(t, paused.Components[0].Forced)
	require.GreaterOrEqual(t, time.Since(before), 1500*time.Millisecond)
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "completed\n", string(remaining))
	require.Equal(t, "paused", runRuntime(t, path, "healthcheck").Components[0].State)
	require.Equal(t, "paused", runRuntime(t, path, "start").Components[0].State)
	resumed := runRuntime(t, path, "resume")
	require.True(t, resumed.Components[0].Ready)
	require.NotEqual(t, started.Components[0].PID, resumed.Components[0].PID)
	again, err := os.ReadFile(rootPath)
	require.NoError(t, err)
	require.Equal(t, root, again)
	metadata, err := json.Marshal(resumed)
	require.NoError(t, err)
	require.NotContains(t, string(metadata), values["MOOX_HEALTH_AUTH_SECRET_KEY"])
}
