package healthz

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

func TestAuthenticatorRequiresValidFreshUniqueSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	var called atomic.Int32
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called.Add(1); w.WriteHeader(http.StatusNoContent) }))

	nonce := strings.Repeat("a", 64)
	req := signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), nonce, "monitor", "secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent || called.Load() != 1 {
		t.Fatalf("valid request = %d called=%d", rr.Code, called.Load())
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), nonce, "monitor", "secret"))
	if rr.Code != http.StatusUnauthorized || called.Load() != 1 {
		t.Fatalf("replay = %d called=%d", rr.Code, called.Load())
	}
}

func TestAuthenticatorRejectsInvalidRequests(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	tests := []struct {
		name string
		req  *http.Request
	}{
		{name: "missing", req: httptest.NewRequest(http.MethodGet, "/healthz", nil)},
		{name: "expired", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Add(-2*time.Minute).Unix(), strings.Repeat("b", 64), "monitor", "secret")},
		{name: "future", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Add(2*time.Minute).Unix(), strings.Repeat("c", 64), "monitor", "secret")},
		{name: "wrong access key", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), strings.Repeat("d", 64), "other", "secret")},
		{name: "wrong path", req: func() *http.Request {
			r := signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), strings.Repeat("e", 64), "monitor", "secret")
			r.URL.Path = "/healthz"
			return r
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, tt.req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", rr.Code)
			}
		})
	}
}

func TestAuthenticatorConsumesConcurrentNonceOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), strings.Repeat("f", 64), "monitor", "secret"))
			if rr.Code == http.StatusNoContent {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("success = %d", success.Load())
	}
}

func TestAuthenticatorFailsClosedAtNonceCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 1})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i, nonce := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), nonce, "monitor", "secret"))
		want := http.StatusNoContent
		if i == 1 {
			want = http.StatusServiceUnavailable
		}
		if rr.Code != want {
			t.Fatalf("request %d status=%d want=%d", i, rr.Code, want)
		}
	}
}

func signedHealthRequest(t *testing.T, method, path string, timestamp int64, nonce, accessKey, secret string) *http.Request {
	t.Helper()
	signature, err := requestauth.Sign(secret, requestauth.Material{Method: method, Path: path, Timestamp: timestamp, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Moox-Health-Auth", "moox-health-v1/"+accessKey+"/"+strconv.FormatInt(timestamp, 10)+"/"+nonce+"/"+signature)
	return req
}

// reset 权限由服务端的密钥映射决定：只有用重置专用密钥签名的请求带 reset 权限，运行时密钥没有；
// 客户端不能自称权限，重置密钥必须成对配置并且不能和运行时密钥相同。
func TestResetPermissionComesFromTheServerSideKeyMapping(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := AuthConfig{Version: "moox-health-v1", AccessKey: "runtime", SecretKey: "runtime-secret", ResetAccessKey: "ops", ResetSecretKey: "ops-secret", MaxNonces: 16}
	a, err := NewAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	var got atomic.Int32 // 1 = 带 reset 权限，2 = 不带
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestauth.HasPermission(r.Context(), requestauth.PermissionReset) {
			got.Store(1)
		} else {
			got.Store(2)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(nonceByte, access, secret string) int {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodPost, "/v1/lifecycle/reset/apply", now.Unix(), strings.Repeat(nonceByte, 64), access, secret))
		return rr.Code
	}

	if code := call("a", "runtime", "runtime-secret"); code != http.StatusNoContent || got.Load() != 2 {
		t.Fatalf("运行时密钥应通过且没有 reset 权限：%d %d", code, got.Load())
	}
	if code := call("b", "ops", "ops-secret"); code != http.StatusNoContent || got.Load() != 1 {
		t.Fatalf("重置专用密钥应带 reset 权限：%d %d", code, got.Load())
	}
	if code := call("c", "ops", "runtime-secret"); code != http.StatusUnauthorized {
		t.Fatalf("重置访问密钥配运行时密钥签名应被拒绝：%d", code)
	}
	if code := call("d", "stranger", "ops-secret"); code != http.StatusUnauthorized {
		t.Fatalf("未知访问密钥应被拒绝：%d", code)
	}

	for name, bad := range map[string]AuthConfig{
		"只配一半":   {Version: "v", AccessKey: "a", SecretKey: "s", ResetAccessKey: "ops"},
		"和运行时相同": {Version: "v", AccessKey: "a", SecretKey: "s", ResetAccessKey: "a", ResetSecretKey: "x"},
	} {
		if _, err := NewAuthenticator(bad); err == nil {
			t.Fatalf("%s：应拒绝这样的配置", name)
		}
	}
	// 没有配置重置密钥：任何请求都不带 reset 权限。
	plain, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "runtime", SecretKey: "runtime-secret"})
	if err != nil {
		t.Fatal(err)
	}
	plain.now = func() time.Time { return now }
	rr := httptest.NewRecorder()
	plain.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestauth.HasPermission(r.Context(), requestauth.PermissionReset) {
			t.Error("没有配置重置密钥时不应有 reset 权限")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/x", now.Unix(), strings.Repeat("e", 64), "runtime", "runtime-secret"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rr.Code)
	}
}
