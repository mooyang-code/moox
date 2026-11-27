package store

import (
	"context"
	"fmt"
	"time"
)

type EquityPointRecord struct {
	SpaceID          string
	TradingAccountID string
	LogicalAccountID string
	BucketTime       int64
	Equity           string
	AvailableFunds   string
	UsedMargin       string
	UnrealizedPnL    *string
	SourceTime       int64
}

// EquityRetentionTier 描述一段历史时间窗的降采样粒度：
// 距离现在处于 [MinAge, MaxAge)（MaxAge == 0 表示无上限）的净值点按 BucketWidth 降采样，
// 每个时间桶仅保留最后一个点。
type EquityRetentionTier struct {
	MinAge      time.Duration
	MaxAge      time.Duration
	BucketWidth time.Duration
}

// DefaultEquityRetentionTiers 是分钟净值历史的分层保留与降采样策略：
//   - 0 ~ 7 天：保留完整 1 分钟粒度；
//   - 7 ~ 30 天：降采样为 15 分钟粒度；
//   - 30 ~ 365 天：降采样为 1 小时粒度；
//   - 365 天以上：降采样为 1 天粒度。
var DefaultEquityRetentionTiers = []EquityRetentionTier{
	{MinAge: 7 * 24 * time.Hour, MaxAge: 30 * 24 * time.Hour, BucketWidth: 15 * time.Minute},
	{MinAge: 30 * 24 * time.Hour, MaxAge: 365 * 24 * time.Hour, BucketWidth: time.Hour},
	{MinAge: 365 * 24 * time.Hour, MaxAge: 0, BucketWidth: 24 * time.Hour},
}

func (tx *Tx) UpsertAccountEquityPoint(record EquityPointRecord) error {
	if record.SpaceID == "" || record.TradingAccountID == "" || record.BucketTime <= 0 || record.SourceTime <= 0 {
		return fmt.Errorf("%w: incomplete account equity point", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(execUpsertAccountEquityPoint, record.SpaceID, record.TradingAccountID, record.BucketTime, record.Equity,
		record.AvailableFunds, record.UsedMargin, record.UnrealizedPnL, record.SourceTime).Error)
}

func (tx *Tx) UpsertLogicalAccountEquityPoint(record EquityPointRecord) error {
	if record.SpaceID == "" || record.LogicalAccountID == "" || record.BucketTime <= 0 || record.SourceTime <= 0 {
		return fmt.Errorf("%w: incomplete logical equity point", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(execUpsertLogicalAccountEquityPoint, record.SpaceID, record.LogicalAccountID, record.BucketTime, record.Equity,
		record.AvailableFunds, record.UsedMargin, record.UnrealizedPnL, record.SourceTime).Error)
}

func (s *Store) ListAccountEquityPoints(ctx context.Context, spaceID, tradingAccountID string, fromBucket, toBucket int64) ([]EquityPointRecord, error) {
	var rows []struct {
		SpaceID          string  `gorm:"column:c_space_id"`
		TradingAccountID string  `gorm:"column:c_trading_account_id"`
		BucketTime       int64   `gorm:"column:c_bucket_time"`
		Equity           string  `gorm:"column:c_equity"`
		AvailableFunds   string  `gorm:"column:c_available_funds"`
		UsedMargin       string  `gorm:"column:c_used_margin"`
		UnrealizedPnL    *string `gorm:"column:c_unrealized_pnl"`
		SourceTime       int64   `gorm:"column:c_source_time"`
	}
	db := s.db.WithContext(ctx).Table("t_account_equity_points").Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID)
	if fromBucket > 0 {
		db = db.Where("c_bucket_time >= ?", fromBucket)
	}
	if toBucket > 0 {
		db = db.Where("c_bucket_time <= ?", toBucket)
	}
	if err := db.Order("c_bucket_time").Find(&rows).Error; err != nil {
		return nil, err
	}
	points := make([]EquityPointRecord, 0, len(rows))
	for _, row := range rows {
		points = append(points, EquityPointRecord{SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, BucketTime: row.BucketTime, Equity: row.Equity, AvailableFunds: row.AvailableFunds, UsedMargin: row.UsedMargin, UnrealizedPnL: row.UnrealizedPnL, SourceTime: row.SourceTime})
	}
	return points, nil
}

// ListLogicalAccountEquityPoints 列出组合账户在 [fromBucket, toBucket] 范围内的净值点（升序）。
func (s *Store) ListLogicalAccountEquityPoints(ctx context.Context, spaceID, logicalAccountID string, fromBucket, toBucket int64) ([]EquityPointRecord, error) {
	var rows []struct {
		SpaceID          string  `gorm:"column:c_space_id"`
		LogicalAccountID string  `gorm:"column:c_logical_account_id"`
		BucketTime       int64   `gorm:"column:c_bucket_time"`
		Equity           string  `gorm:"column:c_equity"`
		AvailableFunds   string  `gorm:"column:c_available_funds"`
		UsedMargin       string  `gorm:"column:c_used_margin"`
		UnrealizedPnL    *string `gorm:"column:c_unrealized_pnl"`
		SourceTime       int64   `gorm:"column:c_source_time"`
	}
	db := s.db.WithContext(ctx).Table("t_logical_account_equity_points").Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID)
	if fromBucket > 0 {
		db = db.Where("c_bucket_time >= ?", fromBucket)
	}
	if toBucket > 0 {
		db = db.Where("c_bucket_time <= ?", toBucket)
	}
	if err := db.Order("c_bucket_time").Find(&rows).Error; err != nil {
		return nil, err
	}
	points := make([]EquityPointRecord, 0, len(rows))
	for _, row := range rows {
		points = append(points, EquityPointRecord{SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID, BucketTime: row.BucketTime, Equity: row.Equity, AvailableFunds: row.AvailableFunds, UsedMargin: row.UsedMargin, UnrealizedPnL: row.UnrealizedPnL, SourceTime: row.SourceTime})
	}
	return points, nil
}

// LatestLogicalAccountEquityPoint 只读最后一条，目标换算时不用扫全部分钟点。
func (s *Store) LatestLogicalAccountEquityPoint(ctx context.Context, spaceID, logicalAccountID string) (EquityPointRecord, error) {
	var row struct {
		BucketTime     int64   `gorm:"column:c_bucket_time"`
		Equity         string  `gorm:"column:c_equity"`
		AvailableFunds string  `gorm:"column:c_available_funds"`
		UsedMargin     string  `gorm:"column:c_used_margin"`
		UnrealizedPnL  *string `gorm:"column:c_unrealized_pnl"`
		SourceTime     int64   `gorm:"column:c_source_time"`
	}
	err := s.db.WithContext(ctx).Table("t_logical_account_equity_points").
		Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).
		Order("c_bucket_time DESC").Limit(1).Take(&row).Error
	if err != nil {
		return EquityPointRecord{}, err
	}
	return EquityPointRecord{
		SpaceID: spaceID, LogicalAccountID: logicalAccountID, BucketTime: row.BucketTime, Equity: row.Equity,
		AvailableFunds: row.AvailableFunds, UsedMargin: row.UsedMargin, UnrealizedPnL: row.UnrealizedPnL,
		SourceTime: row.SourceTime,
	}, nil
}

// DownsampleEquityPoints 按 tiers（空时取 DefaultEquityRetentionTiers）对执行账户与组合账户的历史分钟净值点做分批降采样，
// 防止单表随运行天数无限膨胀。每个时间桶仅保留最后一个点；每轮按 batchLimit 分批循环删除直至当前窗口清理完毕，幂等可重复执行。
func (s *Store) DownsampleEquityPoints(ctx context.Context, now time.Time, batchLimit int, tiers ...EquityRetentionTier) (int64, error) {
	if batchLimit <= 0 {
		batchLimit = 2000
	}
	if len(tiers) == 0 {
		tiers = DefaultEquityRetentionTiers
	}
	var total int64
	for _, tier := range tiers {
		if tier.MinAge <= 0 || tier.BucketWidth <= 0 {
			continue
		}
		upperBound := now.UTC().Add(-tier.MinAge).UnixMilli()
		lowerBound := int64(0)
		if tier.MaxAge > 0 {
			if tier.MaxAge <= tier.MinAge {
				continue
			}
			lowerBound = now.UTC().Add(-tier.MaxAge).UnixMilli()
		}
		bucketMS := tier.BucketWidth.Milliseconds()
		for _, query := range []string{execDownsampleAccountEquityPoints, execDownsampleLogicalEquityPoints} {
			for {
				if err := ctx.Err(); err != nil {
					return total, err
				}
				result := s.db.WithContext(ctx).Exec(query, bucketMS, lowerBound, upperBound, batchLimit)
				if err := writeError(result.Error); err != nil {
					return total, err
				}
				total += result.RowsAffected
				if result.RowsAffected < int64(batchLimit) {
					break
				}
			}
		}
	}
	return total, nil
}

// 净值曲线分桶查询：范围内的点按 c_bucket_time / 桶宽分桶，每桶取最后一点，再并上范围内的第一点。
// 参数依次是空间、账户、起、止、桶宽（毫秒）。
const (
	sqlQueryAccountEquityCurve = `
		WITH ranged AS (
			SELECT * FROM t_account_equity_points
			WHERE c_space_id = ? AND c_trading_account_id = ? AND c_bucket_time >= ? AND c_bucket_time <= ?
		)
		SELECT c_bucket_time, c_equity, c_available_funds, c_used_margin, c_unrealized_pnl, c_source_time
		FROM ranged
		WHERE c_bucket_time IN (SELECT MAX(c_bucket_time) FROM ranged GROUP BY c_bucket_time / ?)
			OR c_bucket_time = (SELECT MIN(c_bucket_time) FROM ranged)
		ORDER BY c_bucket_time
	`
	sqlQueryLogicalAccountEquityCurve = `
		WITH ranged AS (
			SELECT * FROM t_logical_account_equity_points
			WHERE c_space_id = ? AND c_logical_account_id = ? AND c_bucket_time >= ? AND c_bucket_time <= ?
		)
		SELECT c_bucket_time, c_equity, c_available_funds, c_used_margin, c_unrealized_pnl, c_source_time
		FROM ranged
		WHERE c_bucket_time IN (SELECT MAX(c_bucket_time) FROM ranged GROUP BY c_bucket_time / ?)
			OR c_bucket_time = (SELECT MIN(c_bucket_time) FROM ranged)
		ORDER BY c_bucket_time
	`
)

// QueryAccountEquityCurve 按桶宽 bucketMS（毫秒）对 [from, to] 内的净值点降采样：每桶取最后一点，范围内的第一点也保留。
func (s *Store) QueryAccountEquityCurve(ctx context.Context, spaceID, tradingAccountID string, from, to, bucketMS int64) ([]EquityPointRecord, error) {
	points, err := s.queryEquityCurve(ctx, sqlQueryAccountEquityCurve, spaceID, tradingAccountID, from, to, bucketMS)
	for i := range points {
		points[i].SpaceID, points[i].TradingAccountID = spaceID, tradingAccountID
	}
	return points, err
}

// QueryLogicalAccountEquityCurve 同 QueryAccountEquityCurve，查组合账户。
func (s *Store) QueryLogicalAccountEquityCurve(ctx context.Context, spaceID, logicalAccountID string, from, to, bucketMS int64) ([]EquityPointRecord, error) {
	points, err := s.queryEquityCurve(ctx, sqlQueryLogicalAccountEquityCurve, spaceID, logicalAccountID, from, to, bucketMS)
	for i := range points {
		points[i].SpaceID, points[i].LogicalAccountID = spaceID, logicalAccountID
	}
	return points, err
}

// queryEquityCurve 只有管理台的净值曲线用，经只读视图调用。
func (s *Store) queryEquityCurve(ctx context.Context, query, spaceID, ownerID string, from, to, bucketMS int64) ([]EquityPointRecord, error) {
	var rows []struct {
		BucketTime     int64   `gorm:"column:c_bucket_time"`
		Equity         string  `gorm:"column:c_equity"`
		AvailableFunds string  `gorm:"column:c_available_funds"`
		UsedMargin     string  `gorm:"column:c_used_margin"`
		UnrealizedPnL  *string `gorm:"column:c_unrealized_pnl"`
		SourceTime     int64   `gorm:"column:c_source_time"`
	}
	if err := s.db.WithContext(ctx).Raw(query, spaceID, ownerID, from, to, bucketMS).Scan(&rows).Error; err != nil {
		return nil, err
	}
	points := make([]EquityPointRecord, 0, len(rows))
	for _, row := range rows {
		points = append(points, EquityPointRecord{BucketTime: row.BucketTime, Equity: row.Equity, AvailableFunds: row.AvailableFunds, UsedMargin: row.UsedMargin, UnrealizedPnL: row.UnrealizedPnL, SourceTime: row.SourceTime})
	}
	return points, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	execUpsertAccountEquityPoint = `INSERT INTO t_account_equity_points (
			c_space_id, c_trading_account_id, c_bucket_time, c_equity,
			c_available_funds, c_used_margin, c_unrealized_pnl, c_source_time
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_trading_account_id, c_bucket_time) DO UPDATE SET
			c_equity = excluded.c_equity,
			c_available_funds = excluded.c_available_funds,
			c_used_margin = excluded.c_used_margin,
			c_unrealized_pnl = excluded.c_unrealized_pnl,
			c_source_time = excluded.c_source_time,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_source_time >= c_source_time`
	execUpsertLogicalAccountEquityPoint = `INSERT INTO t_logical_account_equity_points (
			c_space_id, c_logical_account_id, c_bucket_time, c_equity,
			c_available_funds, c_used_margin, c_unrealized_pnl, c_source_time
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_logical_account_id, c_bucket_time) DO UPDATE SET
			c_equity = excluded.c_equity,
			c_available_funds = excluded.c_available_funds,
			c_used_margin = excluded.c_used_margin,
			c_unrealized_pnl = excluded.c_unrealized_pnl,
			c_source_time = excluded.c_source_time,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_source_time >= c_source_time`
	// 按桶宽对 [lowerBound, upperBound) 内的历史净值点降采样：保留每桶最后一个点（也自然保留窗口内账户最新一点），分批删除其余点。
	execDownsampleAccountEquityPoints = `DELETE FROM t_account_equity_points
		WHERE rowid IN (
			WITH ranked AS (
				SELECT rowid AS rid,
					c_bucket_time,
					MAX(c_bucket_time) OVER (PARTITION BY c_space_id, c_trading_account_id, c_bucket_time / ?) AS bucket_keep
				FROM t_account_equity_points
				WHERE c_bucket_time >= ? AND c_bucket_time < ?
			)
			SELECT rid FROM ranked
			WHERE c_bucket_time < bucket_keep
			LIMIT ?
		)`
	execDownsampleLogicalEquityPoints = `DELETE FROM t_logical_account_equity_points
		WHERE rowid IN (
			WITH ranked AS (
				SELECT rowid AS rid,
					c_bucket_time,
					MAX(c_bucket_time) OVER (PARTITION BY c_space_id, c_logical_account_id, c_bucket_time / ?) AS bucket_keep
				FROM t_logical_account_equity_points
				WHERE c_bucket_time >= ? AND c_bucket_time < ?
			)
			SELECT rid FROM ranked
			WHERE c_bucket_time < bucket_keep
			LIMIT ?
		)`
)
