package console

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/webserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 控制台处理器挂在 Admin 自己的 9527 入口后面（不再有 Caddy）：经真实 TLS 连接核对
// RPC 的 JSON 响应带 gzip、下载不压、同源 Origin 照常通过、别的来源被拒、来源 IP 取连接对端。
func TestConsoleBehindAdminWebEntry(t *testing.T) {
	store, current := setupSessionTest(t)
	large := []byte(`{"ret_info":{"code":0},"rows":"` + strings.Repeat("x", 4096) + `"}`)
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return large, nil })
	download := bytes.Repeat([]byte("partition "), 1000)
	RegisterRawHandler("archive", "PartitionDownload", RawHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(download)
	}))
	handler := newTestRouter(t, store, nil, gateway).buildControlRouter()

	pkiDir := filepath.Join(t.TempDir(), "pki")
	ca, err := pki.Open(pkiDir)
	require.NoError(t, err)
	_, err = ca.EnsureCA()
	require.NoError(t, err)
	_, root, err := ca.CACertificate()
	require.NoError(t, err)
	require.NoError(t, ca.Close())
	server, err := webserver.New(webserver.Config{Listen: "127.0.0.1:0", Address: "127.0.0.1", PKIDir: pkiDir, CertDir: filepath.Join(t.TempDir(), "web-cert")},
		handler, fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	require.NoError(t, err)
	require.NoError(t, server.Start())
	t.Cleanup(func() { _ = server.Shutdown() })
	base := "https://" + server.Addr().String()
	pool := x509.NewCertPool()
	pool.AddCert(root)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}

	call := func(method, path, origin string, body []byte) (*http.Response, []byte) {
		request, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Accept-Encoding", "gzip")
		request.Header.Set("X-Space-Id", "space-1")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: current.ID})
		response, err := client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response, raw
	}

	response, raw := call(http.MethodPost, "/api/admin/collector/GetTaskList", base, []byte(`{}`))
	require.Equal(t, http.StatusOK, response.StatusCode, string(raw))
	require.Equal(t, "gzip", response.Header.Get("Content-Encoding"), "接口的 JSON 响应带 gzip")
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	plain, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, large, plain, "只压一次")
	assert.NotEmpty(t, response.Header.Get("Content-Security-Policy"), "入口的安全响应头也在接口上")

	response, _ = call(http.MethodPost, "/api/admin/collector/GetTaskList", "https://evil.example", []byte(`{}`))
	assert.Equal(t, http.StatusForbidden, response.StatusCode, "别的来源的页面发来的 POST 被拒")

	response, raw = call(http.MethodGet, "/api/admin/archive/PartitionDownload?space_id=space-1", "", nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Empty(t, response.Header.Get("Content-Encoding"), "下载不压缩")
	assert.Equal(t, download, raw)
}
