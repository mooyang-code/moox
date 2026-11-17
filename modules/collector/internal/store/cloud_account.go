package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CloudAccount 是一个云账号：Collector 调腾讯云用的账号，密钥本身在 Admin 的 SecretMgr，CredentialSecretID 指向它。
type CloudAccount struct {
	ID                 int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	AccountID          string    `gorm:"column:c_account_id"`
	AccountName        string    `gorm:"column:c_account_name"`
	Provider           string    `gorm:"column:c_provider"`
	CredentialSecretID string    `gorm:"column:c_credential_secret_id"`
	AppID              string    `gorm:"column:c_app_id"`
	COSRegion          string    `gorm:"column:c_cos_region"`
	COSBucket          string    `gorm:"column:c_cos_bucket"`
	CreateTime         time.Time `gorm:"column:c_ctime"`
	ModifyTime         time.Time `gorm:"column:c_mtime"`
}

func (CloudAccount) TableName() string { return "t_collector_cloud_accounts" }

// CloudAccountRepository 保存云账号。
type CloudAccountRepository struct{ db *gorm.DB }

func NewCloudAccountRepository(db *gorm.DB) *CloudAccountRepository {
	return &CloudAccountRepository{db: db}
}

// List 按创建先后倒序返回全部云账号。
func (r *CloudAccountRepository) List(ctx context.Context) ([]CloudAccount, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("云账号仓库未初始化")
	}
	var accounts []CloudAccount
	err := r.db.WithContext(ctx).Order("c_id DESC").Find(&accounts).Error
	return accounts, err
}

// Get 返回一个云账号；不存在时返回 nil。
func (r *CloudAccountRepository) Get(ctx context.Context, accountID string) (*CloudAccount, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("云账号仓库未初始化")
	}
	var account CloudAccount
	if err := r.db.WithContext(ctx).Where("c_account_id = ?", strings.TrimSpace(accountID)).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &account, nil
}

// Upsert 按账号 ID 新建或整行更新一个云账号。
func (r *CloudAccountRepository) Upsert(ctx context.Context, account CloudAccount) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("云账号仓库未初始化")
	}
	now := time.Now().UTC()
	account.ID = 0
	account.CreateTime, account.ModifyTime = now, now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_account_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_account_name", "c_provider", "c_credential_secret_id", "c_app_id", "c_cos_region", "c_cos_bucket", "c_mtime",
		}),
	}).Create(&account).Error
}

// Delete 删除一个云账号；账号不存在时不报错。
func (r *CloudAccountRepository) Delete(ctx context.Context, accountID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("云账号仓库未初始化")
	}
	return r.db.WithContext(ctx).Where("c_account_id = ?", strings.TrimSpace(accountID)).Delete(&CloudAccount{}).Error
}
