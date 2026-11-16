package client

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// fakeLoginConsole 按管理台的 Cookie 会话协议应答：登录下发 Cookie，取用户信息和退出要带 Cookie 与本站 Origin。
type fakeLoginConsole struct {
	loginRequest pb.LoginReq
	paths        []string
	loggedOut    bool
}

func (f *fakeLoginConsole) serve(w http.ResponseWriter, request *http.Request) {
	f.paths = append(f.paths, request.URL.Path)
	if request.Header.Get("Origin") != "https://"+request.Host && request.Header.Get("Origin") != "http://"+request.Host {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	cookie, cookieErr := request.Cookie(sessionCookie)
	switch request.URL.Path {
	case "/api/admin/auth/Login":
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &f.loginRequest)
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "recognizable-session-id", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
		response, _ := protojson.Marshal(&pb.LoginRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		_, _ = w.Write(response)
	case "/api/admin/auth/GetUserInfo":
		if cookieErr != nil || cookie.Value != "recognizable-session-id" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		response, _ := protojson.Marshal(&pb.GetUserInfoRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, UserInfo: &pb.UserInfo{Username: f.loginRequest.GetUsername()}})
		_, _ = w.Write(response)
	case "/api/admin/auth/Logout":
		f.loggedOut = cookieErr == nil && cookie.Value == "recognizable-session-id"
		response, _ := protojson.Marshal(&pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		_, _ = w.Write(response)
	default:
		http.NotFound(w, request)
	}
}

func TestVerifyPublicLoginUsesCookieSessionAndLogsOut(t *testing.T) {
	const password = "recognizable-admin-password"
	console := &fakeLoginConsole{}
	server := httptest.NewServer(http.HandlerFunc(console.serve))
	defer server.Close()

	result, err := verifyPublicLogin(context.Background(), server.URL, "admin", password, server.Client())
	require.NoError(t, err)
	assert.Equal(t, LoginResult{LoginAPI: "valid"}, result)
	assert.Equal(t, "admin", console.loginRequest.GetUsername())
	assert.Equal(t, password, console.loginRequest.GetPassword(), "密码明文交给 HTTPS 保护")
	assert.Equal(t, []string{"/api/admin/auth/Login", "/api/admin/auth/GetUserInfo", "/api/admin/auth/Logout"}, console.paths)
	assert.True(t, console.loggedOut, "校验用的会话要退出")
	assert.NotContains(t, result.LoginAPI, "recognizable-session-id")
}

func TestVerifyPublicLoginTrustsOnlyFetchedCaddyCA(t *testing.T) {
	console := &fakeLoginConsole{}
	server := httptest.NewTLSServer(http.HandlerFunc(console.serve))
	defer server.Close()
	_, err := verifyPublicLogin(context.Background(), server.URL, "admin", "password", &http.Client{})
	require.Error(t, err)
	caPath := filepath.Join(t.TempDir(), "root.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	require.NoError(t, os.WriteFile(caPath, caPEM, 0o600))
	result, err := VerifyPublicLoginWithCAFile(context.Background(), server.URL, "admin", "password", caPath)
	require.NoError(t, err)
	assert.Equal(t, "valid", result.LoginAPI)
}

func TestVerifyPublicLoginReturnsStableSecretFreeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ret_info":{"code":4,"msg":"recognizable-admin-password"}}`)
	}))
	defer server.Close()

	_, err := verifyPublicLogin(context.Background(), server.URL, "admin", "recognizable-admin-password", server.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login_verification_failed")
	assert.NotContains(t, err.Error(), "recognizable-admin-password")
}

// 登录成功但没有下发会话 Cookie（例如前面还是旧版管理台）也算校验失败。
func TestVerifyPublicLoginRequiresSessionCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response, _ := protojson.Marshal(&pb.LoginRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		_, _ = w.Write(response)
	}))
	defer server.Close()
	_, err := verifyPublicLogin(context.Background(), server.URL, "admin", "password", server.Client())
	require.ErrorContains(t, err, "login_verification_failed")
}
