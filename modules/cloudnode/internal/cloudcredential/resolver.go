package cloudcredential

import (
	"context"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

type TencentCredential struct {
	SecretID  string
	SecretKey string
}

// Resolver resolves cloud account references through SecretMgr on every operation.
type Resolver struct {
	getValue func(context.Context, *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error)
}

// New borrows the process gateway; every resolution reads the current secret.
func New(gateway gatewayclient.Invoker) (*Resolver, error) {
	if gateway == nil {
		return nil, fmt.Errorf("cloud credential resolver requires the process gateway client")
	}
	return &Resolver{getValue: func(ctx context.Context, req *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
		var response adminpb.GetSecretValueRsp
		if err := gateway.Invoke(ctx, "trpc.moox.ops.SecretMgr", "GetSecretValue", req, &response); err != nil {
			return nil, err
		}
		return &response, nil
	}}, nil
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
	if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || response.GetSecret() == nil {
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
