// Package activitylog 保存各模块写入的、需要展示给用户的活动日志。
package activitylog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxEntriesPerAppend = 200
	maxTitleBytes       = 500
	maxDetailBytes      = 64 << 10
	maxFieldBytes       = 200
	defaultPageSize     = 50
	maxPageSize         = 200
	timeLayout          = "2006-01-02 15:04:05"
)

// Entry 是一条待写入的活动日志。
type Entry struct {
	SpaceID  string
	Source   string
	Category string
	Level    string
	Title    string
	Detail   string
	RefID    string
	DedupKey string
}

// Item 是查询返回的一条活动日志。
type Item struct {
	ID        int64
	SpaceID   string
	Source    string
	Category  string
	Level     string
	Title     string
	Detail    string
	RefID     string
	CreatedAt time.Time
}

// Filter 是查询条件；Since 与 Until 为零值表示不限。Global 为 true 时只查不属于任何空间的全局事件。
type Filter struct {
	SpaceID  string
	Global   bool
	Source   string
	Category string
	RefID    string
	Since    time.Time
	Until    time.Time
	Page     int
	Size     int
}

type logRow struct {
	ID       int64  `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID  string `gorm:"column:c_space_id"`
	Source   string `gorm:"column:c_source"`
	Category string `gorm:"column:c_category"`
	Level    string `gorm:"column:c_level"`
	Title    string `gorm:"column:c_title"`
	Detail   string `gorm:"column:c_detail"`
	RefID    string `gorm:"column:c_ref_id"`
	DedupKey string `gorm:"column:c_dedup_key"`
	CTime    string `gorm:"column:c_ctime"`
}

func (logRow) TableName() string { return "t_activity_logs" }

// DAO 读写 t_activity_logs。
type DAO struct{ db *gorm.DB }

func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

func normalizeEntry(entry Entry) (Entry, error) {
	entry.SpaceID, entry.Source, entry.Category = strings.TrimSpace(entry.SpaceID), strings.TrimSpace(entry.Source), strings.TrimSpace(entry.Category)
	entry.Level, entry.Title, entry.RefID, entry.DedupKey = strings.ToLower(strings.TrimSpace(entry.Level)), strings.TrimSpace(entry.Title), strings.TrimSpace(entry.RefID), strings.TrimSpace(entry.DedupKey)
	if entry.Source == "" || entry.Category == "" || entry.Title == "" {
		return Entry{}, fmt.Errorf("activity log requires source, category and title")
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	if entry.Level != "info" && entry.Level != "warning" && entry.Level != "error" {
		return Entry{}, fmt.Errorf("activity log level must be info, warning or error")
	}
	if len(entry.Title) > maxTitleBytes || len(entry.SpaceID) > maxFieldBytes || len(entry.Source) > maxFieldBytes || len(entry.Category) > maxFieldBytes || len(entry.RefID) > maxFieldBytes || len(entry.DedupKey) > maxFieldBytes {
		return Entry{}, fmt.Errorf("activity log field is too long")
	}
	if strings.TrimSpace(entry.Detail) == "" {
		entry.Detail = "{}"
	}
	if len(entry.Detail) > maxDetailBytes || !json.Valid([]byte(entry.Detail)) {
		return Entry{}, fmt.Errorf("activity log detail must be valid JSON no larger than %d bytes", maxDetailBytes)
	}
	return entry, nil
}

// Append 批量追加；相同 DedupKey 的重放被忽略，返回真正新写入的条数。整批在一个事务中完成。
func (d *DAO) Append(ctx context.Context, entries []Entry, now time.Time) (int, error) {
	if len(entries) == 0 || len(entries) > maxEntriesPerAppend {
		return 0, fmt.Errorf("activity log append requires 1..%d entries", maxEntriesPerAppend)
	}
	rows := make([]logRow, 0, len(entries))
	stamp := now.UTC().Format(timeLayout)
	for _, entry := range entries {
		normalized, err := normalizeEntry(entry)
		if err != nil {
			return 0, err
		}
		rows = append(rows, logRow{SpaceID: normalized.SpaceID, Source: normalized.Source, Category: normalized.Category, Level: normalized.Level, Title: normalized.Title, Detail: normalized.Detail, RefID: normalized.RefID, DedupKey: normalized.DedupKey, CTime: stamp})
	}
	inserted := 0
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows[i])
			if result.Error != nil {
				return result.Error
			}
			inserted += int(result.RowsAffected)
		}
		return nil
	})
	return inserted, err
}

// List 按时间倒序分页查询，返回总数。
func (d *DAO) List(ctx context.Context, filter Filter) ([]Item, int64, error) {
	query := d.db.WithContext(ctx).Model(&logRow{})
	if filter.Global {
		query = query.Where("c_space_id = ''")
	} else if filter.SpaceID != "" {
		query = query.Where("c_space_id = ?", filter.SpaceID)
	}
	if filter.Source != "" {
		query = query.Where("c_source = ?", filter.Source)
	}
	if filter.Category != "" {
		query = query.Where("c_category = ?", filter.Category)
	}
	if filter.RefID != "" {
		query = query.Where("c_ref_id = ?", filter.RefID)
	}
	if !filter.Since.IsZero() {
		query = query.Where("c_ctime >= ?", filter.Since.UTC().Format(timeLayout))
	}
	if !filter.Until.IsZero() {
		query = query.Where("c_ctime < ?", filter.Until.UTC().Format(timeLayout))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	size := filter.Size
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	page := max(filter.Page, 1)
	var rows []logRow
	if err := query.Order("c_id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		created := parseStoredTime(row.CTime)
		items = append(items, Item{ID: row.ID, SpaceID: row.SpaceID, Source: row.Source, Category: row.Category, Level: row.Level, Title: row.Title, Detail: row.Detail, RefID: row.RefID, CreatedAt: created})
	}
	return items, total, nil
}

// parseStoredTime 解析驱动返回的 DATETIME 文本，兼容 RFC3339 与 SQLite 默认格式。
func parseStoredTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, timeLayout, "2006-01-02 15:04:05.999999999-07:00"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
