package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	appconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	authimpl "github.com/mooyang-code/moox/modules/admin/internal/service/auth/impl"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const flowPassword = "pw123"

// newSessionFlowRouter 用真实的认证服务（SQLite 里有一个用户 admin）接到控制台路由上。
func newSessionFlowRouter(t *testing.T, ttl time.Duration) (*mux.Router, *session.Store) {
	t.Helper()
	// 登录接口默认 1 QPS，这里放宽，测的是锁定而不是限流。
	SetConfig(&Config{RateLimit: RateLimitConfig{MethodLimits: map[string]MethodLimit{"/api/admin/auth/Login": {QPS: 1000, Burst: 1000}}}})
	manager := database.NewManager()
	require.NoError(t, manager.Initialize(&appconfig.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}))
	db := manager.GetDB()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	hash, err := mooxsecurity.HashPassword(flowPassword)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{UserID: "user-1", Username: "admin", PasswordHash: hash}).Error)

	sessions, err := session.NewStore(ttl)
	require.NoError(t, err)
	auth, err := authimpl.InitAuthServiceImpl(&authcfg.Config{
		Session:  authcfg.SessionConfig{TTL: ttl},
		Security: authcfg.SecurityConfig{MaxLoginAttempt: 5, LockDuration: 15 * time.Minute},
	}, manager, sessions)
	require.NoError(t, err)
	local, err := NewLocalDispatcher()
	require.NoError(t, err)
	require.NoError(t, local.Register(&pb.AuthServer_ServiceDesc, auth))
	return newTestRouter(t, sessions, local, forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
		t.Fatal("认证服务不应经网关")
		return nil, nil
	})).buildControlRouter(), sessions
}

type flowResult struct {
	code    int
	retCode pb.ErrorCode
	msg     string
	body    string
	cookies []*http.Cookie
}

func flowCall(t *testing.T, router *mux.Router, path, body, cookie, forwardedFor string) flowResult {
	t.Helper()
	r := browserRequest(t, session.Session{ID: cookie}, path, "", []byte(body))
	r.RemoteAddr = "127.0.0.1:40000" // Caddy 在本机
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	var parsed struct {
		RetInfo struct {
			Code pb.ErrorCode `json:"code"`
			Msg  string       `json:"msg"`
		} `json:"ret_info"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &parsed)
	return flowResult{code: rr.Code, retCode: parsed.RetInfo.Code, msg: parsed.RetInfo.Msg, body: rr.Body.String(), cookies: rr.Result().Cookies()}
}

func loginBody(password string) string {
	raw, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	return string(raw)
}

// 登录、带 Cookie 取用户、退出、退出后旧 Cookie 失效，全程用真实认证服务。
func TestBrowserSessionLifecycleWithRealAuthService(t *testing.T) {
	router, sessions := newSessionFlowRouter(t, 24*time.Hour)

	login := flowCall(t, router, "/api/admin/auth/Login", loginBody(flowPassword), "", "198.51.100.7")
	require.Equal(t, http.StatusOK, login.code)
	require.Equal(t, pb.ErrorCode_SUCCESS, login.retCode, login.msg)
	require.Len(t, login.cookies, 1)
	cookie := login.cookies[0]
	assert.Equal(t, session.CookieName, cookie.Name)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, 24*3600, cookie.MaxAge)
	assert.NotContains(t, login.body, cookie.Value, "会话 ID 不进响应体")
	assert.NotContains(t, login.body, flowPassword)
	stored, ok := sessions.Get(cookie.Value)
	require.True(t, ok)
	assert.Equal(t, "user-1", stored.UserID)

	info := flowCall(t, router, "/api/admin/auth/GetUserInfo", `{}`, cookie.Value, "")
	require.Equal(t, http.StatusOK, info.code)
	require.Equal(t, pb.ErrorCode_SUCCESS, info.retCode)
	assert.Contains(t, info.body, `"username":"admin"`)
	assert.Contains(t, info.body, "198.51.100.7", "上次登录 IP 是 Caddy 追加的地址")

	logout := flowCall(t, router, "/api/admin/auth/Logout", `{}`, cookie.Value, "")
	require.Equal(t, http.StatusOK, logout.code)
	require.Equal(t, pb.ErrorCode_SUCCESS, logout.retCode)
	require.Len(t, logout.cookies, 1)
	assert.Equal(t, session.CookieName, logout.cookies[0].Name)
	assert.Equal(t, -1, logout.cookies[0].MaxAge, "退出时让浏览器删掉 Cookie")
	_, ok = sessions.Get(cookie.Value)
	assert.False(t, ok, "服务端会话已删除")

	after := flowCall(t, router, "/api/admin/auth/GetUserInfo", `{}`, cookie.Value, "")
	assert.Equal(t, http.StatusUnauthorized, after.code)
}

// 会话到期即失效（不随使用顺延）；有效期留足 1 秒，避免机器忙时第一次请求就超时。
func TestBrowserSessionExpires(t *testing.T) {
	router, sessions := newSessionFlowRouter(t, time.Second)
	created, err := sessions.Create("user-1", "admin")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, flowCall(t, router, "/api/admin/auth/GetUserInfo", `{}`, created.ID, "").code)
	time.Sleep(time.Until(created.ExpiresAt) + 100*time.Millisecond)
	assert.Equal(t, http.StatusUnauthorized, flowCall(t, router, "/api/admin/auth/GetUserInfo", `{}`, created.ID, "").code)
}

// 经 HTTP 入口连续输错 5 次锁定；锁定按 Caddy 追加的最后一段 IP 计，客户端改第一段绕不过去；别的 IP 不受影响。
func TestBrowserLoginLockoutKeysOnLastForwardedFor(t *testing.T) {
	router, _ := newSessionFlowRouter(t, time.Hour)
	for attempt := 1; attempt <= 5; attempt++ {
		wrong := flowCall(t, router, "/api/admin/auth/Login", loginBody("wrong"), "", "10.0.0."+string(rune('0'+attempt))+", 203.0.113.9")
		require.Equal(t, "用户名或密码错误", wrong.msg, "第 %d 次", attempt)
		require.Empty(t, wrong.cookies)
	}
	locked := flowCall(t, router, "/api/admin/auth/Login", loginBody(flowPassword), "", "6.6.6.6, 203.0.113.9")
	assert.Equal(t, pb.ErrorCode_NO_AUTH, locked.retCode)
	assert.Equal(t, "账户已被锁定，请稍后再试", locked.msg)
	assert.Empty(t, locked.cookies)

	other := flowCall(t, router, "/api/admin/auth/Login", loginBody(flowPassword), "", "203.0.113.10")
	assert.Equal(t, pb.ErrorCode_SUCCESS, other.retCode)
	assert.Len(t, other.cookies, 1)
	assert.False(t, strings.Contains(other.body, other.cookies[0].Value))
}
