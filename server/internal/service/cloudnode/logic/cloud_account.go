package logic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
)

// CloudAccountService 云账户服务接口
type CloudAccountService interface {
	// CreateAccount 创建云账户
	CreateAccount(ctx context.Context, account *model.CloudAccount) error
	// UpdateAccount 更新云账户
	UpdateAccount(ctx context.Context, account *model.CloudAccount) error
	// DeleteAccount 删除云账户
	DeleteAccount(ctx context.Context, accountID string) error
	// GetAccount 获取云账户
	GetAccount(ctx context.Context, accountID string) (*model.CloudAccount, error)
	// GetAccountWithoutMask 获取云账户（不脱敏，仅供内部使用）
	GetAccountWithoutMask(ctx context.Context, accountID string) (*model.CloudAccount, error)
	// ListAccounts 获取云账户列表
	ListAccounts(ctx context.Context) ([]*model.CloudAccount, error)
	// ListAccountsByProvider 根据云厂商获取账户列表
	ListAccountsByProvider(ctx context.Context, provider string) ([]*model.CloudAccount, error)
}

type cloudAccountServiceImpl struct {
	accountDAO dao.CloudAccountDAO
}

// NewCloudAccountService 创建新的云账户服务实例
func NewCloudAccountService(db *gorm.DB) CloudAccountService {
	return &cloudAccountServiceImpl{
		accountDAO: dao.NewCloudAccountDAO(db),
	}
}

// CreateAccount 创建云账户
func (s *cloudAccountServiceImpl) CreateAccount(ctx context.Context, account *model.CloudAccount) error {
	// 验证必填字段
	if account.AccountID == "" {
		return fmt.Errorf("account id is required")
	}
	if account.AccountName == "" {
		return fmt.Errorf("account name is required")
	}
	if account.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if account.SecretID == "" {
		return fmt.Errorf("secret id is required")
	}
	if account.SecretKey == "" {
		return fmt.Errorf("secret key is required")
	}

	// 验证云厂商类型
	switch account.Provider {
	case model.CloudProviderTencent, model.CloudProviderAliyun, model.CloudProviderAWS:
		// 支持的云厂商
	default:
		return fmt.Errorf("unsupported provider: %s", account.Provider)
	}

	// 验证额外配置JSON格式
	if account.ExtraConfig != "" {
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(account.ExtraConfig), &config); err != nil {
			return fmt.Errorf("invalid extra config format: %w", err)
		}
	} else {
		account.ExtraConfig = "{}"
	}

	// 创建账户
	if err := s.accountDAO.CreateCloudAccount(ctx, account); err != nil {
		return fmt.Errorf("failed to create cloud account: %w", err)
	}

	return nil
}

// UpdateAccount 更新云账户
func (s *cloudAccountServiceImpl) UpdateAccount(ctx context.Context, account *model.CloudAccount) error {
	if account.AccountID == "" {
		return fmt.Errorf("account id is required")
	}

	// 检查账户是否存在
	existing, err := s.accountDAO.GetCloudAccount(ctx, account.AccountID)
	if err != nil {
		return fmt.Errorf("failed to check existing account: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("cloud account not found")
	}

	// 验证必填字段
	if account.AccountName == "" {
		account.AccountName = existing.AccountName
	}
	if account.Provider == "" {
		account.Provider = existing.Provider
	}
	if account.SecretID == "" {
		account.SecretID = existing.SecretID
	}

	// 如果SecretKey是脱敏的，使用原来的值
	if account.IsMasked() {
		account.SecretKey = existing.SecretKey
	}

	// 验证额外配置JSON格式
	if account.ExtraConfig != "" {
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(account.ExtraConfig), &config); err != nil {
			return fmt.Errorf("invalid extra config format: %w", err)
		}
	}

	// 更新账户
	if err := s.accountDAO.UpdateCloudAccount(ctx, account); err != nil {
		return fmt.Errorf("failed to update cloud account: %w", err)
	}

	return nil
}

// DeleteAccount 删除云账户
func (s *cloudAccountServiceImpl) DeleteAccount(ctx context.Context, accountID string) error {
	if accountID == "" {
		return fmt.Errorf("account id is required")
	}

	// TODO: 检查是否有关联的云函数正在使用此账户

	// 删除账户（软删除）
	if err := s.accountDAO.DeleteCloudAccount(ctx, accountID); err != nil {
		return fmt.Errorf("failed to delete cloud account: %w", err)
	}

	return nil
}

// GetAccount 获取云账户
func (s *cloudAccountServiceImpl) GetAccount(ctx context.Context, accountID string) (*model.CloudAccount, error) {
	if accountID == "" {
		return nil, fmt.Errorf("account id is required")
	}

	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud account: %w", err)
	}

	if account == nil {
		return nil, fmt.Errorf("cloud account not found")
	}

	// 脱敏处理
	account.MaskSecretKey()

	return account, nil
}

// GetAccountWithoutMask 获取云账户（不脱敏，仅供内部使用）
func (s *cloudAccountServiceImpl) GetAccountWithoutMask(ctx context.Context, accountID string) (*model.CloudAccount, error) {
	if accountID == "" {
		return nil, fmt.Errorf("account id is required")
	}

	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud account: %w", err)
	}

	if account == nil {
		return nil, fmt.Errorf("cloud account not found")
	}

	// 不做脱敏处理，返回原始数据
	return account, nil
}

// ListAccounts 获取云账户列表
func (s *cloudAccountServiceImpl) ListAccounts(ctx context.Context) ([]*model.CloudAccount, error) {
	accounts, err := s.accountDAO.GetCloudAccountList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts: %w", err)
	}

	// 批量脱敏处理
	for _, account := range accounts {
		account.MaskSecretKey()
	}

	return accounts, nil
}

// ListAccountsByProvider 根据云厂商获取账户列表
func (s *cloudAccountServiceImpl) ListAccountsByProvider(ctx context.Context, provider string) ([]*model.CloudAccount, error) {
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}

	accounts, err := s.accountDAO.GetCloudAccountsByProvider(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts by provider: %w", err)
	}

	// 批量脱敏处理
	for _, account := range accounts {
		account.MaskSecretKey()
	}

	return accounts, nil
}
