package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"gorm.io/gorm"
)

// CloudAccountDAO 云账户数据访问对象接口
type CloudAccountDAO interface {
	// CreateCloudAccount 创建云账户
	CreateCloudAccount(ctx context.Context, account *model.CloudAccount) error
	// UpdateCloudAccount 更新云账户
	UpdateCloudAccount(ctx context.Context, account *model.CloudAccount) error
	// DeleteCloudAccount 删除云账户（软删除）
	DeleteCloudAccount(ctx context.Context, accountID string) error
	// GetCloudAccount 获取单个云账户
	GetCloudAccount(ctx context.Context, accountID string) (*model.CloudAccount, error)
	// GetCloudAccountList 获取云账户列表
	GetCloudAccountList(ctx context.Context) ([]*model.CloudAccount, error)
	// GetCloudAccountsByProvider 根据提供商获取账户列表
	GetCloudAccountsByProvider(ctx context.Context, provider string) ([]*model.CloudAccount, error)
}

type cloudAccountDAOImpl struct {
	db *gorm.DB
}

// NewCloudAccountDAO 创建新的云账户DAO实例
func NewCloudAccountDAO(db *gorm.DB) CloudAccountDAO {
	return &cloudAccountDAOImpl{db: db}
}

// CreateCloudAccount 创建云账户
func (d *cloudAccountDAOImpl) CreateCloudAccount(ctx context.Context, account *model.CloudAccount) error {
	account.CreateTime = time.Now()
	account.ModifyTime = time.Now()
	account.Invalid = model.InvalidNo

	result := d.db.WithContext(ctx).Create(account)
	if result.Error != nil {
		return fmt.Errorf("failed to create cloud account: %w", result.Error)
	}
	return nil
}

// UpdateCloudAccount 更新云账户
func (d *cloudAccountDAOImpl) UpdateCloudAccount(ctx context.Context, account *model.CloudAccount) error {
	account.ModifyTime = time.Now()

	// 构建更新字段map
	updates := map[string]interface{}{
		"c_account_name": account.AccountName,
		"c_provider":     account.Provider,
		"c_secret_id":    account.SecretID,
		"c_extra_config": account.ExtraConfig,
		"c_mtime":        account.ModifyTime,
	}

	// 如果SecretKey不是脱敏的，则更新
	if !account.IsMasked() && account.SecretKey != "" {
		updates["c_secret_key"] = account.SecretKey
	}

	result := d.db.WithContext(ctx).
		Model(&model.CloudAccount{}).
		Where("c_account_id = ? AND c_invalid = ?", account.AccountID, model.InvalidNo).
		Updates(updates)

	if result.Error != nil {
		return fmt.Errorf("failed to update cloud account: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("cloud account not found or already deleted")
	}
	return nil
}

// DeleteCloudAccount 删除云账户（软删除）
func (d *cloudAccountDAOImpl) DeleteCloudAccount(ctx context.Context, accountID string) error {
	result := d.db.WithContext(ctx).
		Model(&model.CloudAccount{}).
		Where("c_account_id = ? AND c_invalid = ?", accountID, model.InvalidNo).
		Updates(map[string]interface{}{
			"c_invalid": model.InvalidYes,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to delete cloud account: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("cloud account not found or already deleted")
	}
	return nil
}

// GetCloudAccount 获取单个云账户
func (d *cloudAccountDAOImpl) GetCloudAccount(ctx context.Context, accountID string) (*model.CloudAccount, error) {
	var account model.CloudAccount
	result := d.db.WithContext(ctx).
		Where("c_account_id = ? AND c_invalid = ?", accountID, model.InvalidNo).
		First(&account)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cloud account: %w", result.Error)
	}
	return &account, nil
}

// GetCloudAccountList 获取云账户列表
func (d *cloudAccountDAOImpl) GetCloudAccountList(ctx context.Context) ([]*model.CloudAccount, error) {
	var accounts []*model.CloudAccount
	result := d.db.WithContext(ctx).
		Where("c_invalid = ?", model.InvalidNo).
		Order("c_mtime DESC").
		Find(&accounts)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get cloud account list: %w", result.Error)
	}
	return accounts, nil
}

// GetCloudAccountsByProvider 根据提供商获取账户列表
func (d *cloudAccountDAOImpl) GetCloudAccountsByProvider(ctx context.Context, provider string) ([]*model.CloudAccount, error) {
	var accounts []*model.CloudAccount
	result := d.db.WithContext(ctx).
		Where("c_provider = ? AND c_invalid = ?", provider, model.InvalidNo).
		Order("c_mtime DESC").
		Find(&accounts)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get cloud accounts by provider: %w", result.Error)
	}
	return accounts, nil
}
