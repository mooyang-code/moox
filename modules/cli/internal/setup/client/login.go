package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type LoginResult struct {
	LoginAPI string `json:"login_api"`
}

func VerifyPublicLoginWithCAFile(ctx context.Context, baseURL, username, password, caPath string) (LoginResult, error) {
	info, err := os.Lstat(caPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	return verifyPublicLogin(ctx, baseURL, username, password, httpClient)
}

// verifyPublicLogin 像浏览器一样走一遍登录：用户名和密码登录拿到会话 Cookie，带着 Cookie 取一次用户信息，再退出。
// 出错时只返回固定的 login_verification_failed，不带密码、Cookie 或响应内容。
func verifyPublicLogin(ctx context.Context, baseURL, username, password string, httpClient *http.Client) (LoginResult, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(username) == "" || password == "" {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	console := publicConsole{base: baseURL, http: httpClient}
	loginResponse := &pb.LoginRsp{}
	if err := console.post(ctx, "auth/Login", &pb.LoginReq{Username: username, Password: password}, loginResponse); err != nil || !retInfoSuccess(loginResponse.GetRetInfo()) || console.session == nil {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	userResponse := &pb.GetUserInfoRsp{}
	if err := console.post(ctx, "auth/GetUserInfo", &pb.GetUserInfoReq{}, userResponse); err != nil || !retInfoSuccess(userResponse.GetRetInfo()) || userResponse.GetUserInfo().GetUsername() != username {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	// 校验用的会话立即退出，不在管理台留下多余会话。
	_ = console.post(ctx, "auth/Logout", &pb.LogoutReq{}, &pb.LogoutRsp{})
	return LoginResult{LoginAPI: "valid"}, nil
}

// sessionCookie 是管理台的会话 Cookie 名（与 Admin 的 session.CookieName 一致）。
const sessionCookie = "moox_session"

// publicConsole 是带会话 Cookie 的管理台客户端：登录响应里的 Cookie 记下来，之后的请求带上；每个请求带本站 Origin。
type publicConsole struct {
	base    string
	http    *http.Client
	session *http.Cookie
}

func (c *publicConsole) post(ctx context.Context, route string, request, response proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/admin/"+route, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Origin", c.base)
	if c.session != nil {
		httpRequest.AddCookie(c.session)
	}
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	defer httpResponse.Body.Close()
	for _, cookie := range httpResponse.Cookies() {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			c.session = &http.Cookie{Name: cookie.Name, Value: cookie.Value}
		}
	}
	if httpResponse.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxResponseBytes))
		return fmt.Errorf("login_verification_failed")
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return fmt.Errorf("login_verification_failed")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(body, response); err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	return nil
}

func retInfoSuccess(retInfo *pb.RetInfo) bool {
	return retInfo != nil && retInfo.GetCode() == pb.ErrorCode_SUCCESS
}
