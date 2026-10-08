package cloudcredential

import (
	"context"
	"errors"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
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

func TestResolverReportsTransportError(t *testing.T) {
	resolver := &Resolver{getValue: func(context.Context, *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
		return nil, errors.New("服务 trpc.moox.ops.SecretMgr 没有已启用的部署")
	}}
	if _, err := resolver.Resolve(context.Background(), store.CloudAccount{Provider: "tencent", CredentialSecretID: "secret-1"}); err == nil {
		t.Fatal("Resolve succeeded")
	}
	var unconfigured *Resolver
	if _, err := unconfigured.Resolve(context.Background(), store.CloudAccount{Provider: "tencent", CredentialSecretID: "secret-1"}); err == nil {
		t.Fatal("未配置的解析器应当报错")
	}
}
