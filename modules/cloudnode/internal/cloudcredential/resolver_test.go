package cloudcredential

import (
	"context"
	"errors"
	"fmt"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/stretchr/testify/require"
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

type gatewayFunc func(context.Context, string, string, any, any) error

func (f gatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

func TestResolverUsesGatewayOnEveryReadAndLeavesRetryToSharedLayer(t *testing.T) {
	calls := 0
	transportErr := errors.New("response lost")
	gateway := gatewayFunc(func(_ context.Context, service, method string, request, response any) error {
		calls++
		require.Equal(t, "trpc.moox.ops.SecretMgr", service)
		require.Equal(t, "GetSecretValue", method)
		require.Equal(t, "cloud-secret", request.(*adminpb.GetSecretValueReq).GetSecretId())
		if calls == 3 {
			return transportErr
		}
		*response.(*adminpb.GetSecretValueRsp) = adminpb.GetSecretValueRsp{RetInfo: &adminpb.RetInfo{}, Secret: &adminpb.SecretMaterial{
			Category: "cloud", Provider: "tencent", Status: "active", KeyId: "sid", SecretValue: fmt.Sprintf("value-%d", calls),
		}}
		return nil
	})
	resolver, err := New(gateway)
	require.NoError(t, err)
	account := store.CloudAccount{Provider: "tencent", CredentialSecretID: "cloud-secret"}
	for i := 1; i <= 2; i++ {
		credential, err := resolver.Resolve(t.Context(), account)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("value-%d", i), credential.SecretKey)
	}
	_, err = resolver.Resolve(t.Context(), account)
	require.ErrorIs(t, err, transportErr)
	require.Equal(t, 3, calls)
	_, err = New(nil)
	require.Error(t, err)
}
