package provider

import (
	"context"
	"fmt"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudAccountService 云账户服务接口
type CloudAccountService interface {
	GetAccountWithoutMask(ctx context.Context, accountID string) (*CloudAccount, error)
}

// CloudAccount 云账户信息
type CloudAccount struct {
	AccountID   string
	AccountName string
	Provider    string
	SecretID    string
	SecretKey   string
	AppID       string
	COSRegion   string
	COSBucket   string
}

// AccountFactory 云厂商客户端工厂（基于云账户；因为我们需要支持同一个云厂商下有N个独立的账户）
type AccountFactory struct {
	cloudAccountService CloudAccountService
}

// NewAccountFactory 创建云厂商客户端工厂
func NewAccountFactory(cloudAccountService CloudAccountService) *AccountFactory {
	return &AccountFactory{
		cloudAccountService: cloudAccountService,
	}
}

// GetCloudProviderByAccount 根据账户ID按需创建云厂商客户端
// 每次调用都会从数据库查询最新的账户信息并创建新的客户端实例
// 这样可以支持用户在运行时动态添加/修改云账户，无需重启服务
func (f *AccountFactory) GetCloudProviderByAccount(accountID string) Client {
	ctx := trpc.BackgroundContext()

	// 必须指定账户ID
	if accountID == "" {
		log.WarnContext(ctx, "[Provider AccountFactory] 账户ID为空，无法创建云厂商客户端")
		return nil
	}

	// 从数据库获取不脱敏的账户信息
	fullAccount, err := f.cloudAccountService.GetAccountWithoutMask(ctx, accountID)
	if err != nil {
		log.WarnContextf(ctx, "[Provider AccountFactory] 获取云账户(%s)详情失败: %v", accountID, err)
		return nil
	}

	// 验证账户配置
	if fullAccount.Provider == "" {
		log.WarnContextf(ctx, "[Provider AccountFactory] 云账户(%s)未配置云平台类型", accountID)
		return nil
	}

	// 解析云平台类型
	platformType, err := ParseCloudPlatform(fullAccount.Provider)
	if err != nil {
		log.WarnContextf(ctx, "[Provider AccountFactory] 不支持的云平台类型(%s): %v", fullAccount.Provider, err)
		return nil
	}

	// 构建额外配置
	region := fullAccount.COSRegion
	extraConfig := fmt.Sprintf(`{"region":"%s"}`, region)
	if fullAccount.COSBucket != "" && fullAccount.COSRegion != "" {
		extraConfig = fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_region":"%s","cos_app_id":"%s"}`,
			region, fullAccount.COSBucket, fullAccount.COSRegion, fullAccount.AppID)
	}

	// 创建云平台配置
	cloudConfig, err := NewConfig(
		platformType,
		fullAccount.SecretID,
		fullAccount.SecretKey,
		extraConfig,
	)
	if err != nil {
		log.WarnContextf(ctx, "[Provider AccountFactory] 创建云配置失败(%s): %v", fullAccount.AccountName, err)
		return nil
	}

	// 使用工厂方法创建云厂商客户端
	cloudClient, err := NewClient(cloudConfig)
	if err != nil {
		log.WarnContextf(ctx, "[Provider AccountFactory] 创建云厂商客户端失败(%s): %v", fullAccount.AccountName, err)
		return nil
	}

	log.DebugContextf(ctx, "[Provider AccountFactory] 按需创建云厂商客户端 - 平台: %s, 账户: %s, 账户ID: %s",
		fullAccount.Provider, fullAccount.AccountName, fullAccount.AccountID)
	return cloudClient
}
