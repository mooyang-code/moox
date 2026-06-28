package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm/clause"
)

// AppendTrades 追加成交明细（不可变，按 (space_id, trade_id) 幂等去重）。
func (g *GormStore) AppendTrades(ctx context.Context, spaceID string, trades []*service.Trade) error {
	if len(trades) == 0 {
		return nil
	}
	for _, t := range trades {
		t.SpaceID = spaceID
	}
	return g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_trade_id"}},
		DoNothing: true,
	}).Create(trades).Error
}

// ListTrades 分页查询成交明细。
func (g *GormStore) ListTrades(ctx context.Context, spaceID string, f service.TradeFilter, page service.Page) ([]*service.Trade, int, error) {
	q := g.db.WithContext(ctx).Model(&service.Trade{}).Where("c_space_id = ?", spaceID)
	if f.AccountID != "" {
		q = q.Where("c_account_id = ?", f.AccountID)
	}
	if f.OrderID != "" {
		q = q.Where("c_order_id = ?", f.OrderID)
	}
	if f.Symbol != "" {
		q = q.Where("c_symbol = ?", f.Symbol)
	}
	if f.StartTime > 0 {
		q = q.Where("c_traded_at >= ?", f.StartTime)
	}
	if f.EndTime > 0 {
		q = q.Where("c_traded_at <= ?", f.EndTime)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*service.Trade
	if err := q.Order("c_traded_at DESC").Offset(page.Offset()).Limit(page.PageSize).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}
