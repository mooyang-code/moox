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
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	frontendAppID  = "moox_frontend"
	frontendAppKey = "2521e0d21b6be0347b72bca93904a0dd"
)

type LoginResult struct {
	LoginAPI string `json:"login_api"`
}

func VerifyPublicLogin(ctx context.Context, baseURL, username, password string) (LoginResult, error) {
	return verifyPublicLogin(ctx, baseURL, username, password, &http.Client{Timeout: 30 * time.Second})
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

func verifyPublicLogin(ctx context.Context, baseURL, username, password string, httpClient *http.Client) (LoginResult, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(username) == "" || password == "" {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	appInfo := &pb.AppInfo{AppId: frontendAppID, AppKey: frontendAppKey}
	saltResponse := &pb.GetLoginSaltRsp{}
	if err := postPublicProto(ctx, httpClient, baseURL+"/api/admin/auth/GetLoginSalt", &pb.GetLoginSaltReq{
		AppInfo: appInfo, Username: username,
	}, saltResponse); err != nil || !retInfoSuccess(saltResponse.GetRetInfo()) || saltResponse.GetSalt() == "" || saltResponse.GetTimestamp() <= 0 {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	encryptedPassword, err := mooxsecurity.Encrypt(password, saltResponse.GetSalt()+strconv.FormatInt(saltResponse.GetTimestamp(), 10))
	if err != nil {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	loginResponse := &pb.LoginRsp{}
	if err := postPublicProto(ctx, httpClient, baseURL+"/api/admin/auth/Login", &pb.LoginReq{
		AppInfo: appInfo, Username: username, PasswordHash: encryptedPassword,
		Salt: saltResponse.GetSalt(), Timestamp: saltResponse.GetTimestamp(),
		DeviceId: "moox-cli-setup-verification", UserAgent: "moox-cli/setup", ClientIp: "127.0.0.1",
	}, loginResponse); err != nil || !retInfoSuccess(loginResponse.GetRetInfo()) {
		return LoginResult{}, fmt.Errorf("login_verification_failed")
	}
	return LoginResult{LoginAPI: "valid"}, nil
}

func postPublicProto(ctx context.Context, httpClient *http.Client, endpoint string, request, response proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("login_verification_failed")
	}
	defer httpResponse.Body.Close()
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
