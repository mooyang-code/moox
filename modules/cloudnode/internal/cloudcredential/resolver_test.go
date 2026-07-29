package cloudcredential

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestResolverAcceptsActiveTencentCloudSecret(t *testing.T) {
	resolver := &Resolver{getValue: func(context.Context, *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
		return &adminpb.GetSecretValueRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
			Secret: &adminpb.SecretMaterial{
				Category: "cloud", Provider: "tencent", Status: "active", KeyId: "sid", SecretValue: "skey",
			},
		}, nil
	}}
	credential, err := resolver.Resolve(context.Background(), store.CloudAccount{
		Provider: "tencent", CredentialSecretID: "secret-1",
	})
	if err != nil || credential.SecretID != "sid" || credential.SecretKey != "skey" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestResolverRejectsInactiveOrWrongCategory(t *testing.T) {
	resolver := &Resolver{getValue: func(context.Context, *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
		return &adminpb.GetSecretValueRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
			Secret: &adminpb.SecretMaterial{
				Category: "exchange", Provider: "tencent", Status: "inactive", KeyId: "sid", SecretValue: "skey",
			},
		}, nil
	}}
	if _, err := resolver.Resolve(context.Background(), store.CloudAccount{
		Provider: "tencent", CredentialSecretID: "secret-1",
	}); err == nil {
		t.Fatal("Resolve succeeded")
	}
}

func TestNewFromEnvretrievesSecretThroughHTTPServiceGateway(t *testing.T) {
	const (
		keyID      = "cloudnode"
		secretKey  = "gateway-secret"
		caller     = "cloudnode"
		targetNode = "control"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		_, err = gatewayauth.Verify(
			gatewayauth.Credentials{KeyID: keyID, Secret: secretKey, Caller: caller, Expire: time.Minute},
			gatewayauth.Request{
				Method: req.Method, Path: req.URL.EscapedPath(), Body: body,
				TargetNode: targetNode, Caller: caller,
			},
			req.Header,
			time.Now(),
		)
		if err != nil {
			t.Error(err)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		var input adminpb.GetSecretValueReq
		if err := protojson.Unmarshal(body, &input); err != nil || input.GetSecretId() != "cloud-secret" {
			t.Errorf("secret_id=%q err=%v", input.GetSecretId(), err)
			http.Error(w, "input", http.StatusBadRequest)
			return
		}
		raw, err := protojson.Marshal(&adminpb.GetSecretValueRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
			Secret: &adminpb.SecretMaterial{
				Category: "cloud", Provider: "tencent", Status: "active",
				KeyId: "sid", SecretValue: "skey",
			},
		})
		if err != nil {
			t.Error(err)
			http.Error(w, "response", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	t.Setenv("MOOX_SERVICE_GATEWAY_HTTP_URL", server.URL)
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", targetNode)
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", keyID)
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", secretKey)
	t.Setenv("MOOX_GATEWAY_CALLER", caller)
	resolver, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := resolver.Resolve(context.Background(), store.CloudAccount{
		Provider: "tencent", CredentialSecretID: "cloud-secret",
	})
	if err != nil || credential.SecretID != "sid" || credential.SecretKey != "skey" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}
