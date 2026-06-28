package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
)

// CreateChannel 插入交易通道。
func (g *GormStore) CreateChannel(ctx context.Context, spaceID string, c *service.TradeChannel) error {
	c.SpaceID = spaceID
	if c.IsDeleted == "" {
		c.IsDeleted = service.IsDeletedFalse
	}
	return g.db.WithContext(ctx).Create(c).Error
}

// UpdateChannel 更新通道可变字段。
func (g *GormStore) UpdateChannel(ctx context.Context, spaceID string, c *service.TradeChannel) error {
	res := g.db.WithContext(ctx).
		Model(&service.TradeChannel{}).
		Where("c_space_id = ? AND c_channel_id = ? AND "+notDeleted(), spaceID, c.ChannelID).
		Updates(map[string]interface{}{
			"c_channel_name": c.ChannelName,
			"c_status":       c.Status,
			"c_endpoint":     c.Endpoint,
			"c_rate_limit":   c.RateLimit,
			"c_api_key_id":   c.APIKeyID,
			"c_account_id":   c.AccountID,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// DeleteChannel 软删除通道。
func (g *GormStore) DeleteChannel(ctx context.Context, spaceID, channelID string) error {
	res := g.db.WithContext(ctx).
		Model(&service.TradeChannel{}).
		Where("c_space_id = ? AND c_channel_id = ? AND "+notDeleted(), spaceID, channelID).
		Update("c_is_deleted", service.IsDeletedTrue)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return service.ErrNotFound
	}
	return nil
}

// GetChannel 查询单个有效通道。
func (g *GormStore) GetChannel(ctx context.Context, spaceID, channelID string) (*service.TradeChannel, error) {
	var c service.TradeChannel
	if err := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_channel_id = ? AND "+notDeleted(), spaceID, channelID).
		First(&c).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListChannels 分页查询通道。
func (g *GormStore) ListChannels(ctx context.Context, spaceID string, f service.ChannelFilter, page service.Page) ([]*service.TradeChannel, int, error) {
	q := g.db.WithContext(ctx).Model(&service.TradeChannel{}).
		Where("c_space_id = ? AND "+notDeleted(), spaceID)
	if f.AccountID != "" {
		q = q.Where("c_account_id = ?", f.AccountID)
	}
	if f.Exchange != "" {
		q = q.Where("c_exchange = ?", f.Exchange)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []*service.TradeChannel
	if err := q.Order("c_ctime DESC").Offset(page.Offset()).Limit(page.PageSize).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, int(total), nil
}
