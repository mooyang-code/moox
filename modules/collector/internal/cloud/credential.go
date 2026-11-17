package cloud

import (
	"context"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// Credential 是一个云账号的腾讯云 API 密钥。
type Credential struct {
	// AccountID 是云账号标识：腾讯云按子账号限频，限流配额以它为准，同一账号轮换密钥后不变。
	AccountID string
	SecretID  string
	SecretKey string
}

// CredentialResolver 按云账号取它的腾讯云密钥。每次操作都重新取，密钥在管理台轮换后立即生效。
type CredentialResolver interface {
	Resolve(ctx context.Context, account store.CloudAccount) (Credential, error)
}

// GatewayCredentials 经网关调 Admin SecretMgr 的专用方法 GetCloudCredential 读云账号密钥。
// 类别 cloud、厂商 tencent、状态 active 由 SecretMgr 在服务端校验；Collector 读 SecretMgr 只被放行这个方法与下架检测用的 GetReadonlyExchangeKey，拿不到其他类别的密钥。
type GatewayCredentials struct {
	Gateway gatewayclient.Invoker
}

// Resolve 实现 CredentialResolver。
func (g *GatewayCredentials) Resolve(ctx context.Context, account store.CloudAccount) (Credential, error) {
	if g == nil || g.Gateway == nil {
		return Credential{}, fmt.Errorf("没有配置读取云账号密钥用的网关")
	}
	if account.Provider != "tencent" || strings.TrimSpace(account.CredentialSecretID) == "" {
		return Credential{}, fmt.Errorf("云账号 %s 必须是腾讯云账号并登记了密钥 ID", account.AccountID)
	}
	var response adminpb.GetCloudCredentialRsp
	if err := g.Gateway.Invoke(ctx, "trpc.moox.ops.SecretMgr", "GetCloudCredential", &adminpb.GetCloudCredentialReq{SecretId: account.CredentialSecretID}, &response); err != nil {
		return Credential{}, fmt.Errorf("读取云账号 %s 的密钥失败: %w", account.AccountID, err)
	}
	if response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return Credential{}, fmt.Errorf("读取云账号 %s 的密钥被拒绝: %s", account.AccountID, response.GetRetInfo().GetMsg())
	}
	credential := response.GetCredential()
	if strings.TrimSpace(credential.GetKeyId()) == "" || credential.GetSecretValue() == "" {
		return Credential{}, fmt.Errorf("云账号 %s 的密钥不完整", account.AccountID)
	}
	return Credential{AccountID: account.AccountID, SecretID: credential.GetKeyId(), SecretKey: credential.GetSecretValue()}, nil
}
