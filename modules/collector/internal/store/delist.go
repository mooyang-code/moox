package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DelistNotice 是下架检测发现（或手工登记）的一条下架通知。
// （交易所、市场、代码、范围、下架时间）唯一：同一条再次被报告时只更新最后报告时间，不再发布。
type DelistNotice struct {
	Exchange string
	Market   string
	Symbol   string
	Scope    string
	DelistAt time.Time
	Source   string
	EventID  string
	SeenAt   time.Time
}

type delistNoticeRow struct {
	ID          int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	Exchange    string    `gorm:"column:c_exchange"`
	Market      string    `gorm:"column:c_market"`
	Symbol      string    `gorm:"column:c_symbol"`
	Scope       string    `gorm:"column:c_scope"`
	DelistAt    time.Time `gorm:"column:c_delist_at"`
	Source      string    `gorm:"column:c_source"`
	EventID     string    `gorm:"column:c_event_id"`
	FirstSeenAt time.Time `gorm:"column:c_first_seen_at"`
	LastSeenAt  time.Time `gorm:"column:c_last_seen_at"`
}

func (delistNoticeRow) TableName() string { return "t_collector_delist_notices" }

// DelistNoticeRepository 保存已经发现并发布过的下架通知。
type DelistNoticeRepository struct{ db *gorm.DB }

// NewDelistNoticeRepository 创建仓库。
func NewDelistNoticeRepository(db *gorm.DB) *DelistNoticeRepository {
	return &DelistNoticeRepository{db: db}
}

// Register 记录一条下架通知。第一次出现时在同一个事务里把事件写进发件箱（由 Collector 级的中继发布，不丢），返回 true；
// 已经记录过的只更新最后报告时间，返回 false。
func (r *DelistNoticeRepository) Register(ctx context.Context, notice DelistNotice, event OutboxEvent) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("delist notice repository is not ready")
	}
	if strings.TrimSpace(event.EventID) == "" || notice.EventID != event.EventID {
		return false, fmt.Errorf("delist notice requires a matching outbox event id")
	}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := delistNoticeRow{
			Exchange: notice.Exchange, Market: notice.Market, Symbol: notice.Symbol, Scope: notice.Scope, DelistAt: notice.DelistAt.UTC(),
			Source: notice.Source, EventID: notice.EventID, FirstSeenAt: notice.SeenAt.UTC(), LastSeenAt: notice.SeenAt.UTC(),
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return tx.Model(&delistNoticeRow{}).
				Where("c_exchange = ? AND c_market = ? AND c_symbol = ? AND c_scope = ? AND c_delist_at = ?", notice.Exchange, notice.Market, notice.Symbol, notice.Scope, notice.DelistAt.UTC()).
				Update("c_last_seen_at", notice.SeenAt.UTC()).Error
		}
		created = true
		outbox := eventOutboxRow{EventID: event.EventID, SpaceID: event.SpaceID, EventName: event.EventName, SubjectToken: event.SubjectToken, Payload: event.Payload}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox).Error
	})
	return created, err
}
