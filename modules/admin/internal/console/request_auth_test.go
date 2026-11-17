package console

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试里管理台的地址：浏览器发来的 Host 带着 :9527，Origin 是 https://<Host>。
const (
	testHost   = "control.example:9527"
	testOrigin = "https://" + testHost
)

// setupSessionTest 建一张会话表并登录一个用户 u1。
func setupSessionTest(t *testing.T) (*session.Store, session.Session) {
	t.Helper()
	SetConfig(&Config{})
	store, err := session.NewStore(time.Hour)
	require.NoError(t, err)
	created, err := store.Create("u1", "admin")
	require.NoError(t, err)
	return store, created
}

// browserRequest 是浏览器从管理台页面发出的 POST：同源 Origin，带会话 Cookie。
func browserRequest(t *testing.T, current session.Session, path, space string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Host = testHost
	r.Header.Set("Origin", testOrigin)
	if current.ID != "" {
		r.AddCookie(&http.Cookie{Name: session.CookieName, Value: current.ID})
	}
	if space != "" {
		r.Header.Set("X-Space-Id", space)
	}
	return r
}

func TestOriginAllowedRequiresExactlyOneSameSiteOrigin(t *testing.T) {
	request := func(origins ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/admin/auth/Login", nil)
		r.Host = testHost
		for _, origin := range origins {
			r.Header.Add("Origin", origin)
		}
		return r
	}
	assert.True(t, originAllowed(request(testOrigin), nil))
	assert.True(t, originAllowed(request("HTTPS://CONTROL.EXAMPLE:9527"), nil), "大小写不敏感")
	assert.False(t, originAllowed(request(), nil), "缺少 Origin 一律拒绝")
	assert.False(t, originAllowed(request(""), nil))
	assert.False(t, originAllowed(request("null"), nil))
	assert.False(t, originAllowed(request("https://evil.example"), nil))
	assert.False(t, originAllowed(request("http://"+testHost), nil), "协议不同也不是本站")
	assert.False(t, originAllowed(request("https://control.example:9528"), nil), "端口不同也不是本站")
	assert.False(t, originAllowed(request(testOrigin, testOrigin), nil), "多个 Origin 头拒绝")
	assert.True(t, originAllowed(request("http://localhost:9527"), []string{"http://localhost:9527"}), "配置的开发来源")
}

func TestNeedsOriginCheckCoversWritesAndWebSocketHandshake(t *testing.T) {
	post := httptest.NewRequest(http.MethodPost, "/", nil)
	get := httptest.NewRequest(http.MethodGet, "/", nil)
	upgrade := httptest.NewRequest(http.MethodGet, "/", nil)
	upgrade.Header.Set("Upgrade", "websocket")
	assert.True(t, needsOriginCheck(post, "auth/Login"))
	assert.True(t, needsOriginCheck(upgrade, "archive/PartitionDownload"))
	assert.True(t, needsOriginCheck(get, "ssh/WsConnect"), "终端握手按路由也要核对")
	assert.False(t, needsOriginCheck(get, "archive/PartitionDownload"), "下载是普通 GET")
}

// 9527 由 Admin 直接监听：来源 IP 只取连接对端地址，请求头里的转发地址客户端自己就能写，一概不认。
func TestClientIPUsesConnectionPeerOnly(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "203.0.113.9:50000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.4")
	r.Header.Set("X-Client-Ip", "6.6.6.6")
	r.Header.Set("X-Real-IP", "6.6.6.6")
	assert.Equal(t, "203.0.113.9", clientIP(r))
	r.RemoteAddr = "[2001:db8::9]:50000"
	assert.Equal(t, "2001:db8::9", clientIP(r))
}

func TestSessionCookieAttributes(t *testing.T) {
	rr := httptest.NewRecorder()
	expires := time.Now().Add(24 * time.Hour)
	setSessionCookie(rr, session.Session{ID: "sid-value", ExpiresAt: expires}, 86400)
	cookie := rr.Result().Cookies()[0]
	assert.Equal(t, "__Host-moox_session", cookie.Name, "__Host- 前缀：只认 Secure、Path=/、不带 Domain 的 Cookie")
	assert.Empty(t, cookie.Domain)
	assert.Equal(t, "sid-value", cookie.Value)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, 86400, cookie.MaxAge)
	assert.WithinDuration(t, expires, cookie.Expires, time.Second)

	rr = httptest.NewRecorder()
	clearSessionCookie(rr)
	cleared := rr.Result().Cookies()[0]
	assert.Equal(t, session.CookieName, cleared.Name)
	assert.Empty(t, cleared.Value)
	assert.Equal(t, -1, cleared.MaxAge, "Max-Age=0")
	assert.True(t, cleared.HttpOnly)
	assert.True(t, cleared.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cleared.SameSite)
	assert.Equal(t, "/", cleared.Path)
}

func TestRequestSessionReadsOnlyTheCookie(t *testing.T) {
	store, current := setupSessionTest(t)
	router := newTestRouter(t, store, nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	_, ok := router.requestSession(r)
	assert.False(t, ok)
	r.Header.Set("Authorization", current.ID)
	r.Header.Set("X-Access-Token", current.ID)
	_, ok = router.requestSession(r)
	assert.False(t, ok, "请求头里的令牌不再认")
	r.AddCookie(&http.Cookie{Name: session.CookieName, Value: current.ID})
	got, ok := router.requestSession(r)
	require.True(t, ok)
	assert.Equal(t, "u1", got.UserID)
	store.Delete(current.ID)
	_, ok = router.requestSession(r)
	assert.False(t, ok, "服务端删掉的会话立即失效")
}
