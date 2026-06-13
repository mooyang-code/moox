package provider

import (
	"context"
	"fmt"
	"sync"

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
	// clientCache 缓存已创建的云厂商客户端，避免重复创建导致 goroutine 泄漏
	// key: accountID, value: Client
	clientCache map[string]Client
	cacheMu     sync.RWMutex
}

// NewAccountFactory 创建云厂商客户端工厂
func NewAccountFactory(cloudAccountService CloudAccountService) *AccountFactory {
	return &AccountFactory{
		cloudAccountService: cloudAccountService,
		clientCache:         make(map[string]Client),
	}
}

// GetCloudProviderByAccount 根据账户ID获取云厂商客户端
// ��用缓存机制避免重复创建，防止 goroutine 泄漏
// 如果账户配置变更，调用 InvalidateCache 清除缓存后下次调用会重新创建
func (f *AccountFactory) GetCloudProviderByAccount(accountID string) Client {
	ctx := trpc.BackgroundContext()

	// 必须指定账户ID
	if accountID == "" {
		log.WarnContext(ctx, "[Provider AccountFactory] 账户ID为空，无法创建云厂商客户端")
		return nil
	}

	// 先尝试从缓存获取
	f.cacheMu.RLock()
	if client, exists := f.clientCache[accountID]; exists {
		f.cacheMu.RUnlock()
		log.DebugContextf(ctx, "[Provider AccountFactory] 从缓存获取云厂商客户端 - 账户ID: %s", accountID)
		return client
	}
	f.cacheMu.RUnlock()

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

	// 缓存客户端实例
	f.cacheMu.Lock()
	f.clientCache[accountID] = cloudClient
	f.cacheMu.Unlock()

	return cloudClient
}

// InvalidateCache 清除指定账户的客户端缓存
// 当账户配置变更时调用此方法，下次 GetCloudProviderByAccount 会重新创建客户端
func (f *AccountFactory) InvalidateCache(accountID string) {
	f.cacheMu.Lock()
	delete(f.clientCache, accountID)
	f.cacheMu.Unlock()
	log.Infof("[Provider AccountFactory] 清除账户 %s 的客户端缓存", accountID)
}

// InvalidateAllCache 清除所有客户端缓存
func (f *AccountFactory) InvalidateAllCache() {
	f.cacheMu.Lock()
	f.clientCache = make(map[string]Client)
	f.cacheMu.Unlock()
	log.Info("[Provider AccountFactory] 清除所有客户端缓存")
}
