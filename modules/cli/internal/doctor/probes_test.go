package doctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProberRejectsPathsAndOversizedResponses(t *testing.T) {
	prober := HTTPProber{}
	_, err := prober.Get(context.Background(), "http://localhost/admin")
	require.ErrorContains(t, err, "not allowed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxProbeBytes+1)))
	}))
	defer server.Close()
	_, err = prober.Get(context.Background(), server.URL+"/metrics")
	require.ErrorContains(t, err, "exceeds")
}

func TestHTTPProberRejectsRedirectsAndNonLocalHosts(t *testing.T) {
	prober := HTTPProber{}
	_, err := prober.Get(context.Background(), "http://example.com/healthz")
	require.ErrorContains(t, err, "host")

	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls++
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/healthz", http.StatusFound)
	}))
	defer redirect.Close()
	_, err = prober.Get(context.Background(), redirect.URL+"/healthz")
	require.ErrorContains(t, err, "redirect")
	require.Zero(t, targetCalls)
}

// 健康端口不验签：探测请求不带任何鉴权头，只请求本机回环地址。
func TestHTTPProberSendsNoCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Values("Authorization"))
		for name := range r.Header {
			require.False(t, strings.HasPrefix(strings.ToLower(name), "x-moox-"), "探测不应带 %s", name)
		}
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()
	result, err := HTTPProber{}.Get(context.Background(), server.URL+"/readyz")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.Equal(t, "http://127.0.0.1:11409/readyz", LoopbackURL(11409, "/readyz"))
}

// 没有 moox.toml 时只能探测本节点；探测别的节点报不可达，诊断据此记为无法判断而不是组件故障。
func TestLocalHealthProbeOnlyProbesTheLocalNode(t *testing.T) {
	_, err := LocalHealthProbe("control")(context.Background(), "factor", 11417, "/readyz")
	require.ErrorIs(t, err, ErrNodeUnreachable)
}

func TestProbeWritablePathAlwaysCleansUp(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data", "factor")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, ProbeWritablePath(cancelled, root, "data/factor"))
	matches, err := filepath.Glob(filepath.Join(dir, probePrefix+"*"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.Error(t, ProbeWritablePath(context.Background(), root, "../outside"))
}

func TestProbeWritablePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "data", "escaped")))
	require.ErrorContains(t, ProbeWritablePath(context.Background(), root, "data/escaped"), "escapes release root")
	matches, err := filepath.Glob(filepath.Join(outside, probePrefix+"*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}
