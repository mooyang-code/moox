package dao

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// CreateAccount 插入账户。
func (g *GormStore) CreateAccount(ctx context.Context, spaceID string, a *service.Account) error {
	a.SpaceID = spaceID
	if a.IsDeleted == "" {
		a.IsDeleted = service.IsDeletedFalse
	}
	return g.db.WithContext(ctx).Create(a).Error
}

// UpdateAccount 更新账户基础信息（按 account_id + space_id 定位）。
func (g *GormStore) UpdateAccount(ctx context.Context, spaceID string, a *service.Account) error {
	res := g.db.WithContext(ctx).
		Model(&service.Account{}).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, a.AccountID).
		Updates(map[string]any{
			"c_account_name":  a.AccountName,
			"c_status":        a.Status,
			"c_is_default":    a.IsDefault,
			"c_remark":        a.Remark,
			"c_channel_id":    a.ChannelID,
			"c_base_currency": a.BaseCurrency,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// DeleteAccount 软删除账户。
func (g *GormStore) DeleteAccount(ctx context.Context, spaceID, accountID string) error {
	res := g.db.WithContext(ctx).
		Model(&service.Account{}).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, accountID).
		Update("c_is_deleted", service.IsDeletedTrue)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// GetAccount 查询单个有效账户。
func (g *GormStore) GetAccount(ctx context.Context, spaceID, accountID string) (*service.Account, error) {
	var a service.Account
	if err := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, accountID).
		First(&a).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

// ListAccounts 分页查询账户。
func (g *GormStore) ListAccounts(ctx context.Context, spaceID string, f service.AccountFilter, page service.Page) ([]*service.Account, int, error) {
	q := g.db.WithContext(ctx).Model(&service.Account{}).
		Where("c_space_id = ? AND "+notDeleted(), spaceID)
	if f.UserID != "" {
		q = q.Where("c_user_id = ?", f.UserID)
	}
	if f.AccountType != "" {
		q = q.Where("c_account_type = ?", string(f.AccountType))
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("c_account_name LIKE ? OR c_account_id LIKE ? OR c_remark LIKE ?", like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*service.Account
	if err := q.Order("c_ctime DESC").Offset(page.Offset()).Limit(page.PageSize).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}
