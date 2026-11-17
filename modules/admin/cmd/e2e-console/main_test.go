package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBusinessOKRequiresCompleteJSONWithZeroRetCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		raw  string
		want bool
	}{
		{"success", 200, `{"ret_info":{"code":0,"msg":"ok"},"x":1}`, true},
		{"spaced success", 200, `{"ret_info":{"code":0, "msg":""}}`, true},
		{"business error", 200, `{"ret_info":{"code":1,"msg":"bad"}}`, false},
		{"empty body", 200, ``, false},
		{"truncated", 200, `{"ret_info":{"code":0`, false},
		{"no ret_info", 200, `{"ok":true}`, false},
		{"http error", 403, `{"ret_info":{"code":0}}`, false},
	} {
		if got := businessOK(tc.code, tc.raw); got != tc.want {
			t.Errorf("%s: businessOK = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetMessageOnlyReturnsMessage(t *testing.T) {
	got := retMessage(`{"ret_info":{"msg":"用户名或密码错误"},"user_info":{"username":"admin"}}`)
	if got != "用户名或密码错误" {
		t.Fatalf("retMessage = %q", got)
	}
	if retMessage(`{"user_info":{"username":"admin"}`) != "(响应不可解析)" {
		t.Fatal("不可解析响应不得回显内容")
	}
}

// 登录后后续请求带上会话 Cookie 与本站 Origin。
func TestClientCarriesSessionCookieAndOrigin(t *testing.T) {
	var seen []*http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		if r.URL.Path == "/api/admin/auth/Login" {
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "sid-1", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
	}))
	defer server.Close()
	c := &client{base: server.URL, http: server.Client()}
	code, raw := c.call("/api/admin/auth/Login", "", `{}`)
	if !businessOK(code, raw) || c.session == nil {
		t.Fatalf("login failed: %d %s", code, raw)
	}
	c.call("/api/admin/space/ListSpaces", "crypto", `{}`)
	if len(seen) != 2 || seen[1].Header.Get("Origin") != server.URL || seen[1].Header.Get("X-Space-Id") != "crypto" {
		t.Fatalf("second request headers: %v", seen[1].Header)
	}
	cookie, err := seen[1].Cookie(sessionCookie)
	if err != nil || cookie.Value != "sid-1" {
		t.Fatalf("session cookie not sent: %v", err)
	}
	if _, err := seen[0].Cookie(sessionCookie); err == nil {
		t.Fatal("登录请求不应带旧 Cookie")
	}
}
