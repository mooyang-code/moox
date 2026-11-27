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
	ID          int64  `gorm:"column:c_id;primaryKey;autoIncrement"`
	Exchange    string `gorm:"column:c_exchange"`
	Market      string `gorm:"column:c_market"`
	Symbol      string `gorm:"column:c_symbol"`
	Scope       string `gorm:"column:c_scope"`
	DelistAt    int64  `gorm:"column:c_delist_at"`
	Source      string `gorm:"column:c_source"`
	EventID     string `gorm:"column:c_event_id"`
	FirstSeenAt int64  `gorm:"column:c_first_seen_at"`
	LastSeenAt  int64  `gorm:"column:c_last_seen_at"`
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
		delistAt := toUnixMilli(notice.DelistAt)
		seenAt := toUnixMilli(notice.SeenAt)
		if found && latest.DelistAt == delistAt {
			eventID = latest.EventID
			return tx.Model(&delistNoticeRow{}).Where("c_id = ?", latest.ID).Update("c_last_seen_at", seenAt).Error
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
			Exchange: notice.Exchange, Market: notice.Market, Symbol: notice.Symbol, Scope: notice.Scope, DelistAt: delistAt,
			Source: notice.Source, EventID: event.EventID, FirstSeenAt: seenAt, LastSeenAt: seenAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		created, eventID = true, event.EventID
		outbox := eventOutboxRow{EventID: event.EventID, SpaceID: event.SpaceID, EventName: event.EventName, SubjectToken: event.SubjectToken, Payload: event.Payload, CreatedAt: nowUnixMilli()}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outbox).Error
	})
	return created, eventID, err
}

// DelistedSymbols 返回某交易所、某市场、某范围里“最新一条通知的下架时间已经过了 now”的代码集合（交易所写法，大写）。
// 同一代码改过下架时间时有多行，按 c_id 取最新一行；交易状态检测据此把长期留在交易所列表里的下架现货标成退市。
func (r *DelistNoticeRepository) DelistedSymbols(ctx context.Context, exchange, market, scope string, now time.Time) (map[string]bool, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("delist notice repository is not ready")
	}
	var rows []delistNoticeRow
	if err := r.db.WithContext(ctx).Select("c_id", "c_symbol", "c_delist_at").
		Where("c_exchange = ? AND c_market = ? AND c_scope = ?", exchange, market, scope).
		Order("c_id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	nowMs := toUnixMilli(now)
	seen := map[string]bool{}
	delisted := map[string]bool{}
	for _, row := range rows {
		symbol := strings.ToUpper(strings.TrimSpace(row.Symbol))
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		if row.DelistAt <= nowMs {
			delisted[symbol] = true
		}
	}
	return delisted, nil
}
