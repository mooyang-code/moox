package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertPositions 按 (account_id, symbol, pos_side, is_deleted) 覆盖持仓快照。
func (g *GormStore) UpsertPositions(ctx context.Context, spaceID string, positions []*service.Position) error {
	if len(positions) == 0 {
		return nil
	}
	for _, p := range positions {
		p.SpaceID = spaceID
		if p.IsDeleted == "" {
			p.IsDeleted = service.IsDeletedFalse
		}
	}
	return g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_account_id"}, {Name: "c_symbol"}, {Name: "c_pos_side"}, {Name: "c_is_deleted"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_quantity":       gorm.Expr("excluded.c_quantity"),
			"c_avg_price":      gorm.Expr("excluded.c_avg_price"),
			"c_leverage":       gorm.Expr("excluded.c_leverage"),
			"c_margin":         gorm.Expr("excluded.c_margin"),
			"c_liq_price":      gorm.Expr("excluded.c_liq_price"),
			"c_unrealized_pnl": gorm.Expr("excluded.c_unrealized_pnl"),
			"c_realized_pnl":   gorm.Expr("excluded.c_realized_pnl"),
		}),
	}).Create(positions).Error
}

// ListPositions 查询持仓（可按 symbol 过滤）。
func (g *GormStore) ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*service.Position, error) {
	q := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND "+notDeleted(), spaceID, accountID)
	if symbol != "" {
		q = q.Where("c_symbol = ?", symbol)
	}
	var out []*service.Position
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
