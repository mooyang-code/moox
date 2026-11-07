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

// DelistNotice 是下架检测发现（或手工登记）的一条下架通知。
// （交易所、市场、代码、范围、下架时间）唯一：同一条再次被报告时只更新最后报告时间，不再发布。
type DelistNotice struct {
	Exchange string
	Market   string
	Symbol   string
	Scope    string
	DelistAt time.Time
	Source   string
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

// Register 记录一条下架通知：与该标的（交易所、市场、代码、范围）最近一次发布的下架时间相同，只更新最后报告时间，
// 返回已有的事件 ID 和 false；不同（含改回以前出现过的时间）就是一次新的发布：build 按这是该标的第几次发布（revision，从 0 起）
// 生成事件，通知与发件箱事件在同一个事务里写入，由 Collector 级的中继发布（不丢），返回新事件 ID 和 true。
func (r *DelistNoticeRepository) Register(ctx context.Context, notice DelistNotice, build func(revision int) (OutboxEvent, error)) (created bool, eventID string, err error) {
	if r == nil || r.db == nil {
		return false, "", fmt.Errorf("delist notice repository is not ready")
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := "c_exchange = ? AND c_market = ? AND c_symbol = ? AND c_scope = ?"
		args := []any{notice.Exchange, notice.Market, notice.Symbol, notice.Scope}
		var latest delistNoticeRow
		found := true
		if err := tx.Where(key, args...).Order("c_id DESC").Take(&latest).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			found = false
		}
		if found && latest.DelistAt.Equal(notice.DelistAt.UTC()) {
			eventID = latest.EventID
			return tx.Model(&delistNoticeRow{}).Where("c_id = ?", latest.ID).Update("c_last_seen_at", notice.SeenAt.UTC()).Error
		}
		var revision int64
		if err := tx.Model(&delistNoticeRow{}).Where(key, args...).Count(&revision).Error; err != nil {
			return err
		}
		event, err := build(int(revision))
		if err != nil {
			return err
		}
		if strings.TrimSpace(event.EventID) == "" {
			return fmt.Errorf("delist notice requires an outbox event id")
		}
		row := delistNoticeRow{
			Exchange: notice.Exchange, Market: notice.Market, Symbol: notice.Symbol, Scope: notice.Scope, DelistAt: notice.DelistAt.UTC(),
			Source: notice.Source, EventID: event.EventID, FirstSeenAt: notice.SeenAt.UTC(), LastSeenAt: notice.SeenAt.UTC(),
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		created, eventID = true, event.EventID
		outbox := eventOutboxRow{EventID: event.EventID, SpaceID: event.SpaceID, EventName: event.EventName, SubjectToken: event.SubjectToken, Payload: event.Payload}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox).Error
	})
	return created, eventID, err
}
