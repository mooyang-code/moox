package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type arrivalRow struct {
	DatasetID       string    `gorm:"column:c_dataset_id;primaryKey"`
	SnapshotID      string    `gorm:"column:c_snapshot_id;primaryKey"`
	SubjectID       string    `gorm:"column:c_subject_id;primaryKey"`
	Frequency       string    `gorm:"column:c_frequency;primaryKey"`
	PeriodTime      time.Time `gorm:"column:c_period_time;primaryKey"`
	SeriesTag       string    `gorm:"column:c_series_tag;primaryKey"`
	SourceDatasetID string    `gorm:"column:c_source_dataset_id;primaryKey"`
	FieldsJSON      string    `gorm:"column:c_fields_json"`
	Complete        bool      `gorm:"column:c_complete"`
	ModifiedAt      time.Time `gorm:"column:c_mtime"`
}

func (arrivalRow) TableName() string { return "t_merge_arrivals" }

type commitRow struct {
	CommitID   string    `gorm:"column:c_commit_id;primaryKey"`
	DatasetID  string    `gorm:"column:c_dataset_id"`
	SnapshotID string    `gorm:"column:c_snapshot_id"`
	SubjectID  string    `gorm:"column:c_subject_id"`
	Frequency  string    `gorm:"column:c_frequency"`
	PeriodTime time.Time `gorm:"column:c_period_time"`
	SeriesTag  string    `gorm:"column:c_series_tag"`
	Status     string    `gorm:"column:c_status"`
	NodeID     string    `gorm:"column:c_node_id"`
	StoreID    string    `gorm:"column:c_store_id"`
	Sequence   uint64    `gorm:"column:c_sequence"`
	ModifiedAt time.Time `gorm:"column:c_mtime"`
}

func (commitRow) TableName() string { return "t_merge_commits" }

type Options struct {
	Path string
}

type Ledger struct {
	db *gorm.DB
}

func Open(opts Options) (*Ledger, error) {
	path := opts.Path
	if strings.TrimSpace(path) == "" {
		path = "./data/merge/merge.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create merge database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open merge database: %w", err)
	}
	if err := db.Exec(mergeSQL).Error; err != nil {
		_ = closeGorm(db)
		return nil, fmt.Errorf("apply merge schema: %w", err)
	}
	if err := ensureSourceCompletionExpectedColumn(db); err != nil {
		_ = closeGorm(db)
		return nil, err
	}
	if err := ensureCommitReceiptColumns(db); err != nil {
		_ = closeGorm(db)
		return nil, err
	}
	return &Ledger{db: db}, nil
}

func (l *Ledger) Close() error {
	if l == nil {
		return nil
	}
	return closeGorm(l.db)
}

func (l *Ledger) SaveArrival(ctx context.Context, key RowKey, sourceDatasetID string, fields map[string]float64, complete bool) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	row := arrivalRow{
		DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, SubjectID: key.SubjectID, Frequency: key.Frequency,
		PeriodTime: key.PeriodTime.UTC(), SeriesTag: key.SeriesTag, SourceDatasetID: sourceDatasetID,
		FieldsJSON: string(raw), Complete: complete, ModifiedAt: time.Now().UTC(),
	}
	return l.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_dataset_id"}, {Name: "c_snapshot_id"}, {Name: "c_subject_id"}, {Name: "c_frequency"},
			{Name: "c_period_time"}, {Name: "c_series_tag"}, {Name: "c_source_dataset_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"c_fields_json", "c_complete", "c_mtime"}),
	}).Create(&row).Error
}

func (l *Ledger) LoadArrivals(ctx context.Context, key RowKey) (map[string]map[string]float64, map[string]bool, error) {
	var rows []arrivalRow
	if err := l.db.WithContext(ctx).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_period_time = ? AND c_series_tag = ?",
		key.DatasetID, key.SnapshotID, key.SubjectID, key.Frequency, key.PeriodTime.UTC(), key.SeriesTag,
	).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	fields := make(map[string]map[string]float64, len(rows))
	complete := make(map[string]bool, len(rows))
	for _, row := range rows {
		parsed := map[string]float64{}
		if err := json.Unmarshal([]byte(row.FieldsJSON), &parsed); err != nil {
			return nil, nil, err
		}
		fields[row.SourceDatasetID] = parsed
		complete[row.SourceDatasetID] = row.Complete
	}
	return fields, complete, nil
}

func (l *Ledger) HasCommit(ctx context.Context, key RowKey) (bool, error) {
	var count int64
	err := l.db.WithContext(ctx).Model(&commitRow{}).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_period_time = ? AND c_series_tag = ?",
		key.DatasetID, key.SnapshotID, key.SubjectID, key.Frequency, key.PeriodTime.UTC(), key.SeriesTag,
	).Count(&count).Error
	return count > 0, err
}

func (l *Ledger) RecordCommit(ctx context.Context, commitID string, key RowKey, receipt WriteReceipt) error {
	row := commitRow{
		CommitID: commitID, DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, SubjectID: key.SubjectID,
		Frequency: key.Frequency, PeriodTime: key.PeriodTime.UTC(), SeriesTag: key.SeriesTag, Status: "committed",
		NodeID: strings.TrimSpace(receipt.NodeID), StoreID: strings.TrimSpace(receipt.StoreID), Sequence: receipt.Sequence,
		ModifiedAt: time.Now().UTC(),
	}
	return l.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func (l *Ledger) LookupCommit(ctx context.Context, key RowKey) (WriteReceipt, bool, error) {
	var row commitRow
	err := l.db.WithContext(ctx).Where(
		"c_dataset_id = ? AND c_snapshot_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_period_time = ? AND c_series_tag = ?",
		key.DatasetID, key.SnapshotID, key.SubjectID, key.Frequency, key.PeriodTime.UTC(), key.SeriesTag,
	).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return WriteReceipt{}, false, nil
	}
	if err != nil {
		return WriteReceipt{}, false, err
	}
	return WriteReceipt{CommitID: row.CommitID, NodeID: row.NodeID, StoreID: row.StoreID, Sequence: row.Sequence}, true, nil
}

func ensureCommitReceiptColumns(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("merge database is required")
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "c_node_id", ddl: "ALTER TABLE t_merge_commits ADD COLUMN c_node_id TEXT NOT NULL DEFAULT ''"},
		{name: "c_store_id", ddl: "ALTER TABLE t_merge_commits ADD COLUMN c_store_id TEXT NOT NULL DEFAULT ''"},
		{name: "c_sequence", ddl: "ALTER TABLE t_merge_commits ADD COLUMN c_sequence INTEGER NOT NULL DEFAULT 0"},
	} {
		var count int
		if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('t_merge_commits') WHERE name = ?`, column.name).Scan(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		if err := db.Exec(column.ddl).Error; err != nil {
			return err
		}
	}
	return nil
}

func ensureSourceCompletionExpectedColumn(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("merge database is required")
	}
	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM pragma_table_info('t_merge_source_completions') WHERE name = 'c_expected_json'`).Scan(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Exec(`ALTER TABLE t_merge_source_completions ADD COLUMN c_expected_json TEXT NOT NULL DEFAULT '[]'`).Error
}

func closeGorm(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
