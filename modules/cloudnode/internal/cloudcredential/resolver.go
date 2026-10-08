package cloudcredential

import (
	"context"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"trpc.group/trpc-go/trpc-go/client"
)

type TencentCredential struct {
	SecretID  string
	SecretKey string
}

// Resolver resolves cloud account references through SecretMgr on every operation.
type Resolver struct {
	getValue func(context.Context, *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error)
}

// New 用给定的 tRPC 客户端选项（gatewayclient，cloudnode 身份）经 SecretMgr 读取云账号凭据。
// GetSecretValue 是只读方法，连接中断时 gatewayclient 会重试一次。
func New(options []client.Option) *Resolver {
	secrets := adminpb.NewSecretMgrClientProxy(options...)
	return &Resolver{getValue: func(ctx context.Context, req *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
		return secrets.GetSecretValue(ctx, req)
	}}
}

func (r *Resolver) Resolve(ctx context.Context, account store.CloudAccount) (TencentCredential, error) {
	if r == nil || r.getValue == nil {
		return TencentCredential{}, fmt.Errorf("cloud credential resolver is not configured")
	}
	if account.Provider != "tencent" || strings.TrimSpace(account.CredentialSecretID) == "" {
		return TencentCredential{}, fmt.Errorf("cloud account requires tencent provider and credential_secret_id")
	}
	response, err := r.getValue(ctx, &adminpb.GetSecretValueReq{SecretId: account.CredentialSecretID})
	if err != nil {
		return TencentCredential{}, fmt.Errorf("getValue cloud credential: %w", err)
	}
	if response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || response.GetSecret() == nil {
		return TencentCredential{}, fmt.Errorf("getValue cloud credential rejected")
	}
	secret := response.GetSecret()
	if secret.GetStatus() != "active" || secret.GetCategory() != "cloud" || secret.GetProvider() != "tencent" {
		return TencentCredential{}, fmt.Errorf("cloud credential must be active category=cloud provider=tencent")
	}
	if strings.TrimSpace(secret.GetKeyId()) == "" || secret.GetSecretValue() == "" {
		return TencentCredential{}, fmt.Errorf("cloud credential is incomplete")
	}
	return TencentCredential{SecretID: secret.GetKeyId(), SecretKey: secret.GetSecretValue()}, nil
}
