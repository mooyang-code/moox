package tencent

import (
	"context"
	"fmt"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"trpc.group/trpc-go/trpc-go/log"
)

// Provider 腾讯云Provider实现
type Provider struct {
	secretID    string
	secretKey   string
	scfClient   *scf.Client
	region      string
	extraConfig map[string]interface{}
}

// Config 腾讯云配置
type Config struct {
	SecretID    string
	SecretKey   string
	Region      string
	ExtraConfig map[string]interface{}
}

// NewProvider 创建腾讯云Provider（内部使用）
func NewProvider(config *Config) (*Provider, error) {
	if config.SecretID == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("secret id and secret key are required")
	}

	// 获取region配置，默认广州
	region := config.Region
	if region == "" {
		region = DefaultRegion
	}

	// 创建凭证
	credential := common.NewCredential(config.SecretID, config.SecretKey)

	// 配置客户端
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "scf.tencentcloudapi.com"

	// 创建SCF客户端
	client, err := scf.NewClient(credential, region, cpf)
	if err != nil {
		return nil, fmt.Errorf("failed to create scf client: %w", err)
	}

	return &Provider{
		secretID:    config.SecretID,
		secretKey:   config.SecretKey,
		scfClient:   client,
		region:      region,
		extraConfig: config.ExtraConfig,
	}, nil
}


// logInfo 记录信息日志
func (p *Provider) logInfo(ctx context.Context, format string, args ...interface{}) {
	log.InfoContextf(ctx, "[TencentProvider] "+format, args...)
}

// logError 记录错误日志
func (p *Provider) logError(ctx context.Context, format string, args ...interface{}) {
	log.ErrorContextf(ctx, "[TencentProvider] "+format, args...)
}