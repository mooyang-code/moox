package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *CatalogRepository) ListAccounts(ctx context.Context, provider string) ([]CloudAccount, int64, error) {
	q := r.db.WithContext(ctx).Model(&CloudAccount{}).Where("c_is_deleted = ?", false)
	if provider != "" {
		q = q.Where("c_provider = ?", provider)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var accounts []CloudAccount
	err := q.Order("c_id DESC").Find(&accounts).Error
	return accounts, total, err
}

func (r *CatalogRepository) GetAccount(ctx context.Context, accountID string) (*CloudAccount, error) {
	var account CloudAccount
	if err := r.db.WithContext(ctx).Where("c_account_id = ? AND c_is_deleted = ?", accountID, false).First(&account).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &account, nil
}

func (r *CatalogRepository) UpsertAccount(ctx context.Context, account CloudAccount) error {
	now := time.Now().UTC()
	if account.CreateTime.IsZero() {
		account.CreateTime = now
	}
	account.ModifyTime = now
	if account.Provider == "" {
		account.Provider = "tencent"
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_account_id"}, {Name: "c_is_deleted"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_account_name", "c_provider", "c_secret_id", "c_secret_key", "c_app_id",
			"c_cos_region", "c_cos_bucket", "c_extra_config", "c_mtime",
		}),
	}).Create(&account).Error
}

func (r *CatalogRepository) DeleteAccount(ctx context.Context, accountID string) error {
	return r.db.WithContext(ctx).Model(&CloudAccount{}).Where("c_account_id = ?", accountID).Updates(map[string]any{
		"c_is_deleted": true,
		"c_mtime":      time.Now().UTC(),
	}).Error
}
