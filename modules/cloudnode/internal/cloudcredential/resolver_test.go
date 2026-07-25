package cloudcredential

import (
	"context"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
)

func TestResolverAcceptsActiveTencentCloudSecret(t *testing.T) {
	resolver := NewForTest(func(context.Context, *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error) {
		return &adminpb.RevealSecretRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
			Secret: &adminpb.RevealedSecret{
				Category: "cloud", Provider: "tencent", Status: "active", KeyId: "sid", SecretValue: "skey",
			},
		}, nil
	})
	credential, err := resolver.Resolve(context.Background(), store.CloudAccount{
		Provider: "tencent", CredentialSecretID: "secret-1",
	})
	if err != nil || credential.SecretID != "sid" || credential.SecretKey != "skey" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestResolverRejectsInactiveOrWrongCategory(t *testing.T) {
	resolver := NewForTest(func(context.Context, *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error) {
		return &adminpb.RevealSecretRsp{
			RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS},
			Secret: &adminpb.RevealedSecret{
				Category: "exchange", Provider: "tencent", Status: "inactive", KeyId: "sid", SecretValue: "skey",
			},
		}, nil
	})
	if _, err := resolver.Resolve(context.Background(), store.CloudAccount{
		Provider: "tencent", CredentialSecretID: "secret-1",
	}); err == nil {
		t.Fatal("Resolve succeeded")
	}
}
