package store

import (
	"context"
	"fmt"
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

func (tx *Tx) UpsertAccountEquityPoint(record EquityPointRecord) error {
	if record.SpaceID == "" || record.TradingAccountID == "" || record.BucketTime <= 0 || record.SourceTime <= 0 {
		return fmt.Errorf("%w: incomplete account equity point", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_account_equity_points (
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
		WHERE excluded.c_source_time >= c_source_time
	`, record.SpaceID, record.TradingAccountID, record.BucketTime, record.Equity,
		record.AvailableFunds, record.UsedMargin, record.UnrealizedPnL, record.SourceTime).Error)
}

func (tx *Tx) UpsertLogicalAccountEquityPoint(record EquityPointRecord) error {
	if record.SpaceID == "" || record.LogicalAccountID == "" || record.BucketTime <= 0 || record.SourceTime <= 0 {
		return fmt.Errorf("%w: incomplete logical equity point", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_logical_account_equity_points (
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
		WHERE excluded.c_source_time >= c_source_time
	`, record.SpaceID, record.LogicalAccountID, record.BucketTime, record.Equity,
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
