package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SaveOrder 插入订单。
func (g *GormStore) SaveOrder(ctx context.Context, spaceID string, o *service.Order) error {
	o.SpaceID = spaceID
	return g.db.WithContext(ctx).Create(o).Error
}

// UpsertOrders 批量写入交易所订单快照，按系统订单 ID 幂等更新。
func (g *GormStore) UpsertOrders(ctx context.Context, spaceID string, orders []*service.Order) error {
	if len(orders) == 0 {
		return nil
	}
	for _, o := range orders {
		o.SpaceID = spaceID
	}
	return g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_client_order_id"}, {Name: "c_is_deleted"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_exchange_order_id",
			"c_account_id",
			"c_channel_id",
			"c_exchange",
			"c_symbol",
			"c_market_type",
			"c_side",
			"c_pos_side",
			"c_order_type",
			"c_time_in_force",
			"c_price",
			"c_quantity",
			"c_amount",
			"c_filled_qty",
			"c_filled_amount",
			"c_avg_price",
			"c_fee",
			"c_fee_currency",
			"c_status",
			"c_reduce_only",
			"c_trigger_price",
			"c_source",
			"c_reject_reason",
			"c_submitted_at",
			"c_finished_at",
			"c_extra",
		}),
	}).Create(orders).Error
}

// UpdateOrder 更新订单（成交回填/状态推进/改单）。
func (g *GormStore) UpdateOrder(ctx context.Context, spaceID string, o *service.Order) error {
	res := g.db.WithContext(ctx).
		Model(&service.Order{}).
		Where("c_space_id = ? AND c_order_id = ? AND "+notDeleted(), spaceID, o.OrderID).
		Updates(map[string]any{
			"c_exchange_order_id": o.ExchangeOrderID,
			"c_status":            o.Status,
			"c_filled_qty":        o.FilledQty,
			"c_filled_amount":     o.FilledAmount,
			"c_avg_price":         o.AvgPrice,
			"c_fee":               o.Fee,
			"c_fee_currency":      o.FeeCurrency,
			"c_price":             o.Price,
			"c_quantity":          o.Quantity,
			"c_reject_reason":     o.RejectReason,
			"c_finished_at":       o.FinishedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// GetOrder 按 order_id 或 client_order_id 查询订单。
func (g *GormStore) GetOrder(ctx context.Context, spaceID, orderID, clientOrderID string) (*service.Order, error) {
	var o service.Order
	q := g.db.WithContext(ctx).Model(&service.Order{}).
		Where("c_space_id = ? AND "+notDeleted(), spaceID)
	switch {
	case orderID != "":
		q = q.Where("c_order_id = ?", orderID)
	case clientOrderID != "":
		q = q.Where("c_client_order_id = ?", clientOrderID)
	default:
		return nil, service.ErrInvalidParam
	}
	if err := q.First(&o).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	return &o, nil
}

// ListOrders 分页查询订单。
func (g *GormStore) ListOrders(ctx context.Context, spaceID string, f service.OrderFilter, page service.Page) ([]*service.Order, int, error) {
	q := g.db.WithContext(ctx).Model(&service.Order{}).
		Where("c_space_id = ? AND "+notDeleted(), spaceID)
	if f.AccountID != "" {
		q = q.Where("c_account_id = ?", f.AccountID)
	}
	if f.ChannelID != "" {
		q = q.Where("c_channel_id = ?", f.ChannelID)
	}
	if f.Symbol != "" {
		q = q.Where("c_symbol = ?", f.Symbol)
	}
	if f.Status > 0 {
		q = q.Where("c_status = ?", f.Status)
	}
	if f.OnlyOpen {
		q = q.Where("c_status IN ?", []int{1, 2}) // 已提交/部分成交
	}
	if f.StartTime > 0 {
		q = q.Where("c_ctime >= ?", f.StartTime)
	}
	if f.EndTime > 0 {
		q = q.Where("c_ctime <= ?", f.EndTime)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*service.Order
	if err := q.Order("c_ctime DESC").Offset(page.Offset()).Limit(page.PageSize).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}
