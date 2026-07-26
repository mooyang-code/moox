package cloudcredential

import (
	"context"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

type TencentCredential struct {
	SecretID  string
	SecretKey string
}

// Resolver resolves cloud account references through SecretMgr on every operation.
type Resolver struct {
	reveal func(context.Context, *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error)
}

func NewFromEnv() (*Resolver, error) {
	target := gatewayauth.ServiceGatewayTarget("")
	targetNode := gatewayauth.ServiceGatewayNodeID()
	credentials := gatewayauth.CredentialsFromEnv()
	if target == "" || targetNode == "" || credentials.KeyID == "" || credentials.Secret == "" || credentials.Caller == "" {
		return nil, fmt.Errorf("cloud credential resolver requires gateway target, target node, key id, caller and service secret")
	}
	client := adminpb.NewSecretMgrClientProxy(gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)...)
	return &Resolver{reveal: func(ctx context.Context, req *adminpb.RevealSecretReq) (*adminpb.RevealSecretRsp, error) {
		return client.RevealSecret(ctx, req)
	}}, nil
}

func (r *Resolver) Resolve(ctx context.Context, account store.CloudAccount) (TencentCredential, error) {
	if r == nil || r.reveal == nil {
		return TencentCredential{}, fmt.Errorf("cloud credential resolver is not configured")
	}
	if account.Provider != "tencent" || strings.TrimSpace(account.CredentialSecretID) == "" {
		return TencentCredential{}, fmt.Errorf("cloud account requires tencent provider and credential_secret_id")
	}
	response, err := r.reveal(ctx, &adminpb.RevealSecretReq{SecretId: account.CredentialSecretID})
	if err != nil {
		return TencentCredential{}, fmt.Errorf("reveal cloud credential: %w", err)
	}
	if response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || response.GetSecret() == nil {
		return TencentCredential{}, fmt.Errorf("reveal cloud credential rejected")
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
