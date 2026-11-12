package store

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LogRecordRepository 保存各服务上报的重要日志。
//
// 这里的方法不能写 tRPC 日志：Monitor 自己的 Error 日志也会进这张表，写库失败时再打 Error 会循环。
type LogRecordRepository struct{ db *gorm.DB }

func NewLogRecordRepository(db *gorm.DB) *LogRecordRepository {
	return &LogRecordRepository{db: db}
}

// LogRecordFilter 是查询条件；零值字段不参与筛选。
type LogRecordFilter struct {
	HostID    string
	Component string
	// Level 是最低级别：error 只看 Error 及以上，info 连关键事件一起看；空表示不限。
	Level string
	// Keyword 在正文、事件名、代码位置里按子串匹配。
	Keyword string
	Start   time.Time
	End     time.Time
	// BeforeID 大于 0 时只返回主键更小的记录（向更早翻页）。
	BeforeID int64
	Limit    int
}

// MaxLogRecordPage 是一页最多返回的条数。
const MaxLogRecordPage = 200

// InsertBatch 写入一批日志，已经存在的（同一个 RecordKey）跳过。返回实际新写入的条数。
func (r *LogRecordRepository) InsertBatch(ctx context.Context, records []domain.LogRecord) (int64, error) {
	if len(records) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_record_key"}}, DoNothing: true}).
		Create(&records)
	return result.RowsAffected, result.Error
}

// Query 按条件返回日志，新的在前（按主键倒序，主键随写入递增）。
func (r *LogRecordRepository) Query(ctx context.Context, filter LogRecordFilter) ([]domain.LogRecord, error) {
	// 调用方为了判断“后面还有没有”会多要一条，所以这里的上限比一页多 1。
	limit := filter.Limit
	if limit <= 0 || limit > MaxLogRecordPage+1 {
		limit = MaxLogRecordPage + 1
	}
	query := r.filtered(ctx, filter)
	if filter.BeforeID > 0 {
		query = query.Where("c_id < ?", filter.BeforeID)
	}
	var records []domain.LogRecord
	err := query.Order("c_id DESC").Limit(limit).Find(&records).Error
	return records, err
}

// LogBucket 是一个小时里的日志条数：Errors 是 Error 及以上，Events 是其余的（关键事件）。
type LogBucket struct {
	Hour   string
	Errors int64
	Events int64
}

// Buckets 按小时统计符合条件的日志条数（不看 BeforeID 和 Limit），按时间升序，只含有记录的小时。
// Hour 是这个小时的起点（UTC，RFC 3339）。
func (r *LogRecordRepository) Buckets(ctx context.Context, filter LogRecordFilter) ([]LogBucket, error) {
	var buckets []LogBucket
	err := r.filtered(ctx, filter).
		Select("strftime('%Y-%m-%dT%H:00:00Z', c_logged_at) AS hour, "+
			"SUM(CASE WHEN c_level IN ? THEN 1 ELSE 0 END) AS errors, "+
			"SUM(CASE WHEN c_level IN ? THEN 0 ELSE 1 END) AS events", levelsAtOrAbove("error"), levelsAtOrAbove("error")).
		Group("hour").Order("hour").Scan(&buckets).Error
	return buckets, err
}

// filtered 套上除翻页以外的查询条件。
func (r *LogRecordRepository) filtered(ctx context.Context, filter LogRecordFilter) *gorm.DB {
	query := r.db.WithContext(ctx).Model(&domain.LogRecord{})
	if value := strings.TrimSpace(filter.HostID); value != "" {
		query = query.Where("c_host_id = ?", value)
	}
	if value := strings.TrimSpace(filter.Component); value != "" {
		query = query.Where("c_component = ?", value)
	}
	if levels := levelsAtOrAbove(filter.Level); len(levels) > 0 {
		query = query.Where("c_level IN ?", levels)
	}
	if value := strings.TrimSpace(filter.Keyword); value != "" {
		// 关键字按原样匹配：把 LIKE 的通配符转义掉。
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value) + "%"
		query = query.Where(`(c_message LIKE ? ESCAPE '\' OR c_event LIKE ? ESCAPE '\' OR c_caller LIKE ? ESCAPE '\')`, pattern, pattern, pattern)
	}
	if !filter.Start.IsZero() {
		query = query.Where("c_logged_at >= ?", filter.Start.UTC())
	}
	if !filter.End.IsZero() {
		query = query.Where("c_logged_at < ?", filter.End.UTC())
	}
	return query
}

// levelsAtOrAbove 返回不低于 level 的级别名；level 为空或不认识时返回 nil（不筛选）。
func levelsAtOrAbove(level string) []string {
	order := []string{"debug", "info", "warn", "error", "dpanic", "panic", "fatal"}
	level = strings.ToLower(strings.TrimSpace(level))
	for i, name := range order {
		if name == level {
			return order[i:]
		}
	}
	return nil
}

// Prune 删除日志时间早于 cutoff 的记录；之后如果总行数仍超过 maxRows，从最旧的删起。返回删除的行数。
func (r *LogRecordRepository) Prune(ctx context.Context, cutoff time.Time, maxRows int64) (int64, error) {
	deleted, err := DeleteBefore(ctx, r.db, "t_monitor_log_records", "c_logged_at", cutoff)
	if err != nil || maxRows <= 0 {
		return deleted, err
	}
	// 第 maxRows 新的那一行的主键就是保留的下界。
	var boundary struct{ ID int64 }
	err = r.db.WithContext(ctx).Raw("SELECT c_id AS id FROM t_monitor_log_records ORDER BY c_id DESC LIMIT 1 OFFSET ?", maxRows-1).Scan(&boundary).Error
	if err != nil || boundary.ID == 0 {
		return deleted, err
	}
	result := r.db.WithContext(ctx).Exec("DELETE FROM t_monitor_log_records WHERE c_id < ?", boundary.ID)
	return deleted + result.RowsAffected, result.Error
}
