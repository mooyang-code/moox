package client

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestVerifyPublicLoginMatchesBrowserProtocolAndDiscardsSession(t *testing.T) {
	const (
		username  = "admin"
		password  = "recognizable-admin-password"
		salt      = "login-salt"
		timestamp = int64(1700000000)
	)
	var loginRequest pb.LoginReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/admin/auth/GetLoginSalt":
			body, _ := protojson.Marshal(&pb.GetLoginSaltRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Salt: salt, Timestamp: timestamp, ExpiresIn: 300,
			})
			_, _ = w.Write(body)
		case "/api/admin/auth/Login":
			body, _ := io.ReadAll(request.Body)
			_ = protojson.Unmarshal(body, &loginRequest)
			response, _ := protojson.Marshal(&pb.LoginRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, AccessToken: "recognizable-access-token",
				SessionId: "recognizable-session-id", RequestSigningKey: "recognizable-signing-key",
			})
			_, _ = w.Write(response)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	result, err := VerifyPublicLogin(context.Background(), server.URL, username, password)
	require.NoError(t, err)
	assert.Equal(t, LoginResult{LoginAPI: "valid"}, result)
	assert.Equal(t, username, loginRequest.GetUsername())
	assert.Equal(t, "moox_frontend", loginRequest.GetAppInfo().GetAppId())
	assert.NotEqual(t, password, loginRequest.GetPasswordHash())
	plain, err := mooxsecurity.Decrypt(loginRequest.GetPasswordHash(), salt+strconv.FormatInt(timestamp, 10))
	require.NoError(t, err)
	assert.Equal(t, password, plain)
	assert.NotContains(t, result.LoginAPI, "recognizable-access-token")
	assert.NotContains(t, result.LoginAPI, "recognizable-signing-key")
}

func TestVerifyPublicLoginTrustsOnlyFetchedCaddyCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/admin/auth/GetLoginSalt":
			body, _ := protojson.Marshal(&pb.GetLoginSaltRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Salt: "salt", Timestamp: 1})
			_, _ = w.Write(body)
		case "/api/admin/auth/Login":
			body, _ := protojson.Marshal(&pb.LoginRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	_, err := VerifyPublicLogin(context.Background(), server.URL, "admin", "password")
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

	_, err := VerifyPublicLogin(context.Background(), server.URL, "admin", "recognizable-admin-password")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "login_verification_failed")
	assert.NotContains(t, err.Error(), "recognizable-admin-password")
}
