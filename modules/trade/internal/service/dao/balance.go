package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetBalances 查询账户余额；currencies 为空表示返回全部。
func (g *GormStore) GetBalances(ctx context.Context, spaceID, accountID string, currencies []string) ([]*service.Balance, error) {
	q := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, accountID)
	if len(currencies) > 0 {
		q = q.Where("c_currency IN ?", currencies)
	}
	var out []*service.Balance
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpsertBalances 覆盖写入余额快照（交易所同步后调用）。
// 按 (account_id, currency) upsert，不递增 version（整体覆盖）。
func (g *GormStore) UpsertBalances(ctx context.Context, spaceID string, balances []*service.Balance) error {
	if len(balances) == 0 {
		return nil
	}
	for _, b := range balances {
		b.SpaceID = spaceID
		if b.IsDeleted == "" {
			b.IsDeleted = service.IsDeletedFalse
		}
	}
	return g.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, b := range balances {
			// ON CONFLICT (c_account_id, c_currency, c_is_deleted) DO UPDATE
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "c_account_id"}, {Name: "c_currency"}, {Name: "c_is_deleted"}},
				DoUpdates: clause.Assignments(map[string]any{
					"c_available": gorm.Expr("excluded.c_available"),
					"c_frozen":    gorm.Expr("excluded.c_frozen"),
					"c_total":     gorm.Expr("excluded.c_total"),
				}),
			}).Create(b).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
