package webserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

// largeJS 足够大，静态文件会预先压缩。
var largeJS = []byte(strings.Repeat("console.log('moox');\n", 200))

func testSite() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><title>MooX</title>")},
		"static/js/app-abc.js":    {Data: largeJS},
		"static/js/same-a.js":     {Data: []byte("aaaa")},
		"static/js/same-b.js":     {Data: []byte("bbbb")},
		"css/loading.css":         {Data: []byte("body{}")},
		".gitkeep":                {Data: nil},
		"static/_plugin-vue.js":   {Data: []byte("export default 1")},
		"static/img/logo-abc.png": {Data: []byte{0x89, 'P', 'N', 'G'}},
	}
}

// newTestPKI 建一个新的 MooX CA，返回目录和根证书。
func newTestPKI(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pki")
	store, err := pki.Open(dir)
	require.NoError(t, err)
	defer store.Close()
	_, err = store.EnsureCA()
	require.NoError(t, err)
	_, cert, err := store.CACertificate()
	require.NoError(t, err)
	return dir, cert
}

type testServer struct {
	*Server
	ca   *x509.Certificate
	addr string
}

func (s testServer) url(path string) string { return "https://" + s.addr + path }

func (s testServer) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(s.ca)
	return pool
}

// client 只走 HTTP/1.1；h2 为 true 时允许协商 HTTP/2。
func (s testServer) client(h2 bool) *http.Client {
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: s.pool()}, ForceAttemptHTTP2: h2}
	if !h2 {
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func startTestServer(t *testing.T, api http.Handler, site fstest.MapFS, adjust func(*Config)) testServer {
	t.Helper()
	pkiDir, ca := newTestPKI(t)
	cfg := Config{Listen: "127.0.0.1:0", Address: "127.0.0.1", PKIDir: pkiDir, CertDir: filepath.Join(t.TempDir(), "web-cert")}
	if adjust != nil {
		adjust(&cfg)
	}
	if api == nil {
		api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "api") })
	}
	if site == nil {
		site = testSite()
	}
	server, err := New(cfg, api, site)
	require.NoError(t, err)
	require.NoError(t, server.Start())
	t.Cleanup(func() { _ = server.Shutdown() })
	return testServer{Server: server, ca: ca, addr: server.Addr().String()}
}

func get(t *testing.T, client *http.Client, url string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for name, value := range headers {
		if name == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, body
}

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	pkiDir, _ := newTestPKI(t)
	api := http.NotFoundHandler()
	for name, cfg := range map[string]Config{
		"缺地址":    {PKIDir: pkiDir, CertDir: t.TempDir()},
		"地址带端口":  {Address: "203.0.113.10:9527", PKIDir: pkiDir, CertDir: t.TempDir()},
		"监听地址不对": {Listen: "9527", Address: "203.0.113.10", PKIDir: pkiDir, CertDir: t.TempDir()},
		"没有 CA":  {Address: "203.0.113.10", PKIDir: filepath.Join(t.TempDir(), "empty"), CertDir: t.TempDir()},
	} {
		_, err := New(cfg, api, testSite())
		assert.Error(t, err, name)
	}
}

func TestHostMustEqualControlAddress(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	client := server.client(false)
	response, body := get(t, client, server.url("/"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, string(body), "MooX")

	response, body = get(t, client, server.url("/"), map[string]string{"Host": "evil.example:9527"})
	assert.Equal(t, http.StatusNotFound, response.StatusCode, "别的 Host 一律 404")
	assert.NotContains(t, string(body), "MooX")
	assert.Empty(t, response.Header.Get("Content-Security-Policy"))
	response, _ = get(t, client, server.url("/api/admin/auth/GetUserInfo"), map[string]string{"Host": "evil.example"})
	assert.Equal(t, http.StatusNotFound, response.StatusCode, "接口也不放行")

	for _, tc := range []struct {
		host, address string
		want          bool
	}{
		{"203.0.113.10:9527", "203.0.113.10", true},
		{"203.0.113.10", "203.0.113.10", true},
		{"CONSOLE.Example.test:9527", "console.example.test", true},
		{"[2001:db8::1]:9527", "2001:db8::1", true},
		{"[2001:db8:0::1]", "2001:db8::1", true},
		{"203.0.113.11:9527", "203.0.113.10", false},
		{"console.example.test.evil:9527", "console.example.test", false},
		{"", "203.0.113.10", false},
	} {
		assert.Equal(t, tc.want, hostMatches(tc.host, tc.address), "%s vs %s", tc.host, tc.address)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	client := server.client(false)
	for _, path := range []string{"/", "/static/js/app-abc.js", "/api/admin/auth/GetUserInfo", "/ca/root.crt", "/healthz"} {
		response, _ := get(t, client, server.url(path), nil)
		for name, value := range securityHeaders {
			assert.Equal(t, value, response.Header.Get(name), "%s %s", path, name)
		}
	}
	assert.Contains(t, securityHeaders["Content-Security-Policy"], "frame-ancestors 'none'")
}

func TestStaticSPAFallbackCacheAndETag(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	client := server.client(false)

	response, body := get(t, client, server.url("/strategy/running?tab=1"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "<!doctype html><title>MooX</title>", string(body), "前端路由回退到 index.html")
	assert.Equal(t, "text/html; charset=utf-8", response.Header.Get("Content-Type"))
	assert.Contains(t, response.Header.Get("Cache-Control"), "no-store", "入口页不缓存")

	response, _ = get(t, client, server.url("/static/js/missing-abc.js"), nil)
	assert.Equal(t, http.StatusNotFound, response.StatusCode, "缺的静态资源是 404，不回退")
	response, body = get(t, client, server.url("/.gitkeep"), nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Contains(t, string(body), "MooX", "占位文件不对外提供")

	response, body = get(t, client, server.url("/static/_plugin-vue.js"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode, "下划线开头的分块也要能取到")
	assert.Equal(t, "export default 1", string(body))
	assert.Equal(t, "public, max-age=31536000, immutable", response.Header.Get("Cache-Control"))
	assert.Equal(t, "application/javascript; charset=utf-8", response.Header.Get("Content-Type"))
	response, _ = get(t, client, server.url("/css/loading.css"), nil)
	assert.Equal(t, "public, max-age=86400", response.Header.Get("Cache-Control"), "不带哈希的文件只缓存一天")

	first, _ := get(t, client, server.url("/static/js/same-a.js"), nil)
	second, _ := get(t, client, server.url("/static/js/same-b.js"), nil)
	etag := first.Header.Get("ETag")
	require.NotEmpty(t, etag)
	assert.NotEqual(t, etag, second.Header.Get("ETag"), "大小相同、内容不同的文件 ETag 不同")
	response, body = get(t, client, server.url("/static/js/same-a.js"), map[string]string{"If-None-Match": etag})
	assert.Equal(t, http.StatusNotModified, response.StatusCode)
	assert.Empty(t, body)
	response, _ = get(t, client, server.url("/static/js/same-a.js"), map[string]string{"If-None-Match": `"other", ` + etag})
	assert.Equal(t, http.StatusNotModified, response.StatusCode)
	response, _ = get(t, client, server.url("/static/js/same-a.js"), map[string]string{"If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)})
	assert.Equal(t, http.StatusOK, response.StatusCode, "不按修改时间判断")

	request, err := http.NewRequest(http.MethodPost, server.url("/"), nil)
	require.NoError(t, err)
	posted, err := client.Do(request)
	require.NoError(t, err)
	posted.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, posted.StatusCode)
}

func TestStaticWithoutFrontendExplainsHowToBuild(t *testing.T) {
	server := startTestServer(t, nil, fstest.MapFS{".gitkeep": {}}, nil)
	response, body := get(t, server.client(false), server.url("/"), nil)
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, string(body), "scripts/build/build.sh admin")
}

func TestBlockedPathsNeverFallBackToIndex(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	client := server.client(false)
	for _, path := range []string{"/api", "/api/", "/api/other/x", "/healthz", "/readyz", "/readyz/x", "/metrics"} {
		response, body := get(t, client, server.url(path), nil)
		assert.Equal(t, http.StatusNotFound, response.StatusCode, path)
		assert.NotContains(t, string(body), "MooX", path)
	}
	response, body := get(t, client, server.url("/api/admin/x/y"), nil)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "api", string(body), "/api/admin/* 交给控制台处理器")
}

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	plain, err := io.ReadAll(reader)
	require.NoError(t, err)
	return plain
}

func TestStaticGzipAndRawChannelsStayUncompressed(t *testing.T) {
	download := bytes.Repeat([]byte("binary-partition-data "), 500)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(download)
	})
	server := startTestServer(t, api, nil, nil)
	client := server.client(false)

	response, body := get(t, client, server.url("/static/js/app-abc.js"), map[string]string{"Accept-Encoding": "gzip"})
	require.Equal(t, "gzip", response.Header.Get("Content-Encoding"))
	assert.Contains(t, response.Header.Get("Vary"), "Accept-Encoding")
	assert.Equal(t, largeJS, gunzip(t, body))
	gzipETag := response.Header.Get("ETag")
	response, body = get(t, client, server.url("/static/js/app-abc.js"), map[string]string{"Accept-Encoding": "identity"})
	assert.Empty(t, response.Header.Get("Content-Encoding"))
	assert.Equal(t, largeJS, body)
	assert.NotEqual(t, gzipETag, response.Header.Get("ETag"), "压缩前后是两种表示，ETag 不同")
	response, _ = get(t, client, server.url("/static/js/app-abc.js"), map[string]string{"Accept-Encoding": "gzip;q=0"})
	assert.Empty(t, response.Header.Get("Content-Encoding"))

	response, body = get(t, client, server.url("/api/admin/archive/PartitionDownload"), map[string]string{"Accept-Encoding": "gzip"})
	assert.Empty(t, response.Header.Get("Content-Encoding"), "入口不压缩接口与下载，RPC JSON 由控制台处理器自己压")
	assert.Equal(t, download, body)
}

func TestCADownloadServesExactlyTheMooXRoot(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	client := server.client(false)

	response, body := get(t, client, server.url("/ca/root.crt"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, `attachment; filename="moox-ca.crt"`, response.Header.Get("Content-Disposition"))
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	block, rest := pem.Decode(body)
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE", block.Type)
	assert.Empty(t, bytes.TrimSpace(rest), "只输出一张证书")
	assert.Equal(t, server.ca.Raw, block.Bytes, "就是 MooX CA 根证书")
	assert.Equal(t, 1, bytes.Count(body, []byte("-----BEGIN ")))

	response, body = get(t, client, server.url("/ca/root.sha256"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, fingerprint(server.ca)+"\n", string(body))
	assert.Regexp(t, `^[0-9A-F]{2}(:[0-9A-F]{2}){31}\n$`, string(body), "前端只认大写冒号格式")

	response, body = get(t, client, server.url("/ca/moox-root-ca.zip"), nil)
	require.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "application/zip", response.Header.Get("Content-Type"))
	files := unzipAll(t, body)
	assert.Equal(t, server.ca.Raw, mustDecodePEM(t, readZipFile(t, files[caFileName])))
	assert.Contains(t, string(readZipFile(t, files["AGENT-INSTALL.md"])), "https://"+server.addr+"/")

	request, err := http.NewRequest(http.MethodHead, server.url("/ca/root.crt"), nil)
	require.NoError(t, err)
	head, err := client.Do(request)
	require.NoError(t, err)
	headBody, _ := io.ReadAll(head.Body)
	head.Body.Close()
	assert.Equal(t, http.StatusOK, head.StatusCode)
	assert.Empty(t, headBody)
	request, err = http.NewRequest(http.MethodPost, server.url("/ca/root.crt"), nil)
	require.NoError(t, err)
	posted, err := client.Do(request)
	require.NoError(t, err)
	posted.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, posted.StatusCode)
}

func mustDecodePEM(t *testing.T, raw []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(raw)
	require.NotNil(t, block)
	return block.Bytes
}

func TestNegotiatesHTTP2AndHTTP11(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	response, _ := get(t, server.client(true), server.url("/"), nil)
	assert.Equal(t, 2, response.ProtoMajor, "普通请求走 HTTP/2")
	response, _ = get(t, server.client(false), server.url("/"), nil)
	assert.Equal(t, 1, response.ProtoMajor, "只会 HTTP/1.1 的客户端照常可用")
	assert.Equal(t, []string{"h2", "http/1.1"}, server.http.TLSConfig.NextProtos)
}

// 终端的 WebSocket 用 golang.org/x/net/websocket，要直接断言 http.Hijacker，只能在 HTTP/1.1 上升级。
// 入口同时开着 HTTP/2 时，浏览器为 WebSocket 另开一条 HTTP/1.1 连接（服务端不宣告 RFC 8441），升级要能成功。
func TestWebSocketUpgradeOverHTTP11WhileHTTP2IsEnabled(t *testing.T) {
	var hijackable atomic.Bool
	echo := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			var message string
			if websocket.Message.Receive(conn, &message) == nil {
				_ = websocket.Message.Send(conn, "echo:"+message)
			}
		},
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := w.(http.Hijacker)
		hijackable.Store(ok)
		echo.ServeHTTP(w, r)
	})
	server := startTestServer(t, api, nil, nil)

	config, err := websocket.NewConfig("wss://"+server.addr+"/api/admin/ssh/WsConnect?session_id=s1", "https://"+server.addr)
	require.NoError(t, err)
	// 像浏览器一样：WebSocket 的连接只协商 http/1.1，并且照常带着 Accept-Encoding。
	config.TlsConfig = &tls.Config{RootCAs: server.pool(), ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}}
	config.Header = http.Header{"Accept-Encoding": {"gzip, deflate, br"}}
	conn, err := websocket.DialConfig(config)
	require.NoError(t, err, "开着 HTTP/2 时，HTTP/1.1 的 WebSocket 升级要成功")
	defer conn.Close()
	require.NoError(t, websocket.Message.Send(conn, "ls"))
	var reply string
	require.NoError(t, websocket.Message.Receive(conn, &reply))
	assert.Equal(t, "echo:ls", reply)
	assert.True(t, hijackable.Load(), "入口没有包装 ResponseWriter")

	// 服务端的 HTTP/2 SETTINGS 不能宣告 ENABLE_CONNECT_PROTOCOL，否则浏览器会改走 HTTP/2 上的 WebSocket，终端握手失败。
	settings := serverHTTP2Settings(t, server)
	_, announced := settings[0x8]
	assert.False(t, announced && settings[0x8] == 1, "服务端宣告了 RFC 8441：%v", settings)
}

// serverHTTP2Settings 以 h2 握手，读服务端发来的第一个 SETTINGS 帧。
func serverHTTP2Settings(t *testing.T, server testServer) map[uint16]uint32 {
	t.Helper()
	conn, err := tls.Dial("tcp", server.addr, &tls.Config{RootCAs: server.pool(), ServerName: "127.0.0.1", NextProtos: []string{"h2"}})
	require.NoError(t, err)
	defer conn.Close()
	require.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol)
	_, err = conn.Write(append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), 0, 0, 0, 0x4, 0, 0, 0, 0, 0))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for {
		header := make([]byte, 9)
		_, err := io.ReadFull(conn, header)
		require.NoError(t, err)
		length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		payload := make([]byte, length)
		_, err = io.ReadFull(conn, payload)
		require.NoError(t, err)
		if header[3] != 0x4 || header[4]&0x1 != 0 {
			continue
		}
		settings := map[uint16]uint32{}
		for i := 0; i+6 <= len(payload); i += 6 {
			settings[binary.BigEndian.Uint16(payload[i:])] = binary.BigEndian.Uint32(payload[i+2:])
		}
		return settings
	}
}

func serialOf(t *testing.T, certificate *tls.Certificate) string {
	t.Helper()
	require.NotNil(t, certificate)
	require.NotNil(t, certificate.Leaf)
	return certificate.Leaf.SerialNumber.String()
}

func TestCertificateIsReusedUntilThirtyDaysLeftThenHotSwapped(t *testing.T) {
	var now atomic.Pointer[time.Time]
	start := time.Now()
	now.Store(&start)
	server := startTestServer(t, nil, nil, func(cfg *Config) {
		cfg.Now = func() time.Time { return *now.Load() }
	})
	first, err := server.certs.get(nil)
	require.NoError(t, err)
	leaf := first.Leaf
	assert.NoError(t, leaf.VerifyHostname("127.0.0.1"), "SAN 是控制机地址")
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	assert.NoError(t, leaf.CheckSignatureFrom(server.ca), "由 MooX CA 签")
	assert.WithinDuration(t, start.Add(certificateLifetime), leaf.NotAfter, time.Minute, "有效期一年")

	// 剩 31 天：继续用。
	later := start.Add(certificateLifetime - 31*24*time.Hour)
	now.Store(&later)
	require.NoError(t, server.RefreshCertificate(context.Background()))
	current, _ := server.certs.get(nil)
	assert.Equal(t, serialOf(t, first), serialOf(t, current))

	// 剩 29 天：重签，GetCertificate 返回新证书，新连接拿到的也是新证书。
	renew := start.Add(certificateLifetime - 29*24*time.Hour)
	now.Store(&renew)
	require.NoError(t, server.RefreshCertificate(context.Background()))
	current, _ = server.certs.get(nil)
	assert.NotEqual(t, serialOf(t, first), serialOf(t, current))
	conn, err := tls.Dial("tcp", server.addr, &tls.Config{RootCAs: server.pool(), ServerName: "127.0.0.1"})
	require.NoError(t, err)
	served := conn.ConnectionState().PeerCertificates[0]
	conn.Close()
	assert.Equal(t, current.Leaf.SerialNumber, served.SerialNumber, "热替换后新连接用新证书")
}

func TestCertificateOnDiskIsReusedOnlyForSameCAAndAddress(t *testing.T) {
	pkiDir, ca := newTestPKI(t)
	certDir := filepath.Join(t.TempDir(), "web-cert")
	refresh := func(pkiDir, address string) *tls.Certificate {
		certs := &certificates{address: address, pkiDir: pkiDir, dir: certDir, now: time.Now}
		require.NoError(t, certs.Refresh())
		current, err := certs.get(nil)
		require.NoError(t, err)
		return current
	}
	first := refresh(pkiDir, "203.0.113.10")
	assert.Equal(t, serialOf(t, first), serialOf(t, refresh(pkiDir, "203.0.113.10")), "重启后沿用 web-cert/ 里的证书")
	assert.NoError(t, first.Leaf.CheckSignatureFrom(ca))

	changed := refresh(pkiDir, "console.example.test")
	assert.NotEqual(t, serialOf(t, first), serialOf(t, changed), "地址变了就重签")
	assert.NoError(t, changed.Leaf.VerifyHostname("console.example.test"))

	otherPKI, otherCA := newTestPKI(t)
	rotated := refresh(otherPKI, "console.example.test")
	assert.NotEqual(t, serialOf(t, changed), serialOf(t, rotated), "CA 换了（全新部署）就重签")
	assert.NoError(t, rotated.Leaf.CheckSignatureFrom(otherCA))
}

func TestServerTimeouts(t *testing.T) {
	server := startTestServer(t, nil, nil, nil)
	assert.Equal(t, 10*time.Second, server.http.ReadHeaderTimeout)
	assert.Equal(t, 2*time.Minute, server.http.IdleTimeout)
	assert.Zero(t, server.http.WriteTimeout, "下载与终端是长连接，不设写超时")
	assert.Zero(t, server.http.ReadTimeout, "上传是长连接，不设读超时")
}

// 缩短两个时限，确认它们真的作用在 9527 的连接上。
func TestReadHeaderAndIdleTimeoutsCloseConnections(t *testing.T) {
	pkiDir, ca := newTestPKI(t)
	server, err := New(Config{Listen: "127.0.0.1:0", Address: "127.0.0.1", PKIDir: pkiDir, CertDir: filepath.Join(t.TempDir(), "web-cert")}, http.NotFoundHandler(), testSite())
	require.NoError(t, err)
	server.http.ReadHeaderTimeout = 200 * time.Millisecond
	server.http.IdleTimeout = 200 * time.Millisecond
	require.NoError(t, server.Start())
	t.Cleanup(func() { _ = server.Shutdown() })
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	dial := func() *tls.Conn {
		conn, err := tls.Dial("tcp", server.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}})
		require.NoError(t, err)
		return conn
	}
	closedWithin := func(conn *tls.Conn, limit time.Duration) bool {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(limit)))
		_, err := io.ReadAll(conn)
		var timeout net.Error
		return err == nil || !(errors.As(err, &timeout) && timeout.Timeout())
	}

	slow := dial()
	defer slow.Close()
	_, err = io.WriteString(slow, "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n")
	require.NoError(t, err)
	assert.True(t, closedWithin(slow, 3*time.Second), "请求头 10 秒（这里 200ms）没发完就断开")

	idle := dial()
	defer idle.Close()
	_, err = io.WriteString(idle, "GET /css/loading.css HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	require.NoError(t, err)
	assert.True(t, closedWithin(idle, 3*time.Second), "空闲 2 分钟（这里 200ms）后断开保持的连接")
}

func TestShutdownStopsAcceptingAndWaitsForRequestsInFlight(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	server := startTestServer(t, api, nil, func(cfg *Config) { cfg.ShutdownTimeout = 5 * time.Second })
	client := server.client(false)
	result := make(chan string, 1)
	go func() {
		response, err := client.Get(server.url("/api/admin/x/Slow"))
		if err != nil {
			result <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		result <- string(body)
	}()
	<-entered
	stopped := make(chan error, 1)
	began := time.Now()
	go func() { stopped <- server.Shutdown() }()
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", server.addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
		}
		return err != nil
	}, 3*time.Second, 20*time.Millisecond, "停机后不再接新连接")
	close(release)
	assert.Equal(t, "done", <-result, "在途请求照常完成")
	require.NoError(t, <-stopped)
	assert.Less(t, time.Since(began), 5*time.Second)
}

func TestShutdownGivesUpAfterTimeout(t *testing.T) {
	entered := make(chan struct{})
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	server := startTestServer(t, api, nil, func(cfg *Config) { cfg.ShutdownTimeout = 300 * time.Millisecond })
	go func() { _, _ = server.client(false).Get(server.url("/api/admin/x/Hang")) }()
	<-entered
	began := time.Now()
	err := server.Shutdown()
	assert.Error(t, err, "等满时限还有请求没完")
	assert.Less(t, time.Since(began), 3*time.Second, "到时限就断开，不会一直等")
	assert.Equal(t, defaultShutdownTimeout, 30*time.Second)
}
