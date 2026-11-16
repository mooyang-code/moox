package consoleclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 登录请求体是明文用户名和密码；之后每个请求带会话 Cookie 和本站 Origin，登录本身也带 Origin。
func TestClientLogsInWithPasswordAndCarriesSessionCookie(t *testing.T) {
	var login map[string]string
	var origins []string
	var cookies []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins = append(origins, r.Header.Get("Origin"))
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			cookies = append(cookies, cookie.Value)
		} else {
			cookies = append(cookies, "")
		}
		switch r.URL.Path {
		case "/api/admin/auth/Login":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &login)
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "sid-1", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
			_, _ = io.WriteString(w, `{"ret_info":{"code":0}}`)
		default:
			_, _ = io.WriteString(w, `{"ret_info":{"code":0}}`)
		}
	}))
	defer server.Close()
	client, err := New(Options{Address: server.Listener.Addr().String(), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background(), "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	var response struct{}
	if err := client.Call(context.Background(), "setup", "GetConfigBackup", map[string]string{}, &response); err != nil {
		t.Fatal(err)
	}
	if login["username"] != "admin" || login["password"] != "pw" {
		t.Fatalf("登录请求体 = %v", login)
	}
	if origins[0] != server.URL || origins[1] != server.URL {
		t.Fatalf("Origin = %v，要等于 %s", origins, server.URL)
	}
	if cookies[0] != "" || cookies[1] != "sid-1" {
		t.Fatalf("Cookie = %v", cookies)
	}
}

func TestClientLoginWithoutCookieFailsWithoutLeakingResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ret_info":{"code":0},"user_info":{"username":"admin"}}`)
	}))
	defer server.Close()
	client, err := New(Options{Address: server.Listener.Addr().String(), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Login(context.Background(), "admin", "pw")
	if err == nil || !strings.Contains(err.Error(), "会话 Cookie") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("err = %v", err)
	}
	if err := client.Call(context.Background(), "setup", "GetConfigBackup", map[string]string{}, &struct{}{}); err == nil {
		t.Fatal("没登录时不能调用")
	}
}
