package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// SaveOrder 插入订单。
func (g *GormStore) SaveOrder(ctx context.Context, spaceID string, o *service.Order) error {
	o.SpaceID = spaceID
	if o.IsDeleted == "" {
		o.IsDeleted = service.IsDeletedFalse
	}
	return g.db.WithContext(ctx).Create(o).Error
}

// UpdateOrder 更新订单（成交回填/状态推进/改单）。
func (g *GormStore) UpdateOrder(ctx context.Context, spaceID string, o *service.Order) error {
	res := g.db.WithContext(ctx).
		Model(&service.Order{}).
		Where("c_space_id = ? AND c_order_id = ? AND "+notDeleted(), spaceID, o.OrderID).
		Updates(map[string]interface{}{
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
