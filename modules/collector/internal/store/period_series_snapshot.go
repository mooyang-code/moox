package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxPeriodSeriesSnapshotCleanupPeriods = 1000

// ErrPeriodSeriesSnapshotConflict means a period already has a different immutable
// snapshot.
var ErrPeriodSeriesSnapshotConflict = errors.New("collector period series snapshot conflict")

// PeriodSeriesSnapshotRepository persists immutable Dataset period series snapshots.
type PeriodSeriesSnapshotRepository struct {
	db *gorm.DB
}

func NewPeriodSeriesSnapshotRepository(db *gorm.DB) *PeriodSeriesSnapshotRepository {
	return &PeriodSeriesSnapshotRepository{db: db}
}

// GetPeriodSeriesSnapshot returns the saved snapshot ordered by its stable dense index.
// found is false only when no entries exist for the period.
func (r *PeriodSeriesSnapshotRepository) GetPeriodSeriesSnapshot(ctx context.Context, key domain.PeriodKey) (domain.PeriodSeriesSnapshot, bool, error) {
	if r == nil || r.db == nil {
		return domain.PeriodSeriesSnapshot{}, false, fmt.Errorf("period series snapshot repository is not initialized")
	}
	key, err := normalizePeriodSeriesSnapshotKey(key)
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, false, err
	}
	entries, err := loadPeriodSeriesSnapshotEntries(r.db.WithContext(ctx), key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime)
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, false, err
	}
	if len(entries) == 0 {
		return domain.PeriodSeriesSnapshot{}, false, nil
	}
	if _, err = normalizeAndValidatePeriodSeriesSnapshotEntries(entries); err != nil {
		return domain.PeriodSeriesSnapshot{}, true, fmt.Errorf("stored period series snapshot is invalid: %w", err)
	}
	return periodSeriesSnapshotFromEntries(key, entries), true, nil
}

// CreatePeriodSeriesSnapshotIfAbsent atomically installs a snapshot. The unique
// period/index index arbitrates concurrent creators; the winning index-zero
// insert and all remaining rows commit in one transaction. A later caller
// receives the stored rows if every immutable field matches, or a conflict if
// the snapshot differs.
func (r *PeriodSeriesSnapshotRepository) CreatePeriodSeriesSnapshotIfAbsent(ctx context.Context, input domain.PeriodSeriesSnapshot) (domain.PeriodSeriesSnapshot, bool, error) {
	if r == nil || r.db == nil {
		return domain.PeriodSeriesSnapshot{}, false, fmt.Errorf("period series snapshot repository is not initialized")
	}
	desired, err := normalizeAndValidatePeriodSeriesSnapshot(input)
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, false, err
	}
	key := desired.Key
	created := false
	var persisted []domain.PeriodSeriesSnapshotEntry
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		rows := append([]domain.PeriodSeriesSnapshotEntry(nil), desired.Entries...)
		for i := range rows {
			rows[i].CreateTime = now
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 500)
		if result.Error != nil {
			return result.Error
		}
		current, err := loadPeriodSeriesSnapshotEntries(tx, key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime)
		if err != nil {
			return err
		}
		if !samePeriodSeriesSnapshotEntries(current, desired.Entries) {
			return ErrPeriodSeriesSnapshotConflict
		}
		if result.RowsAffected != 0 && result.RowsAffected != int64(len(desired.Entries)) {
			return ErrPeriodSeriesSnapshotConflict
		}
		created = result.RowsAffected == int64(len(desired.Entries))
		persisted = current
		return nil
	})
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, false, err
	}
	return periodSeriesSnapshotFromEntries(key, persisted), created, nil
}

func periodSeriesSnapshotFromEntries(key domain.PeriodKey, entries []domain.PeriodSeriesSnapshotEntry) domain.PeriodSeriesSnapshot {
	return domain.PeriodSeriesSnapshot{Key: key, SeriesHash: entries[0].SeriesHash, ExpectedCount: uint32(entries[0].ExpectedCount), Entries: entries}
}

func normalizeAndValidatePeriodSeriesSnapshot(input domain.PeriodSeriesSnapshot) (domain.PeriodSeriesSnapshot, error) {
	key, err := normalizePeriodSeriesSnapshotKey(input.Key)
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, err
	}
	entries, err := normalizeAndValidatePeriodSeriesSnapshotEntries(input.Entries)
	if err != nil {
		return domain.PeriodSeriesSnapshot{}, err
	}
	first := entries[0]
	if key.SpaceID != first.SpaceID || key.DatasetID != first.DatasetID || key.Frequency != first.Frequency || !key.PeriodTime.Equal(first.PeriodTime) {
		return domain.PeriodSeriesSnapshot{}, fmt.Errorf("period series snapshot key must match its entries")
	}
	if input.ExpectedCount != uint32(len(entries)) {
		return domain.PeriodSeriesSnapshot{}, fmt.Errorf("period series snapshot expected_count must equal entry count")
	}
	if strings.ToLower(strings.TrimSpace(input.SeriesHash)) != first.SeriesHash {
		return domain.PeriodSeriesSnapshot{}, fmt.Errorf("period series snapshot hash must match its entries")
	}
	return periodSeriesSnapshotFromEntries(key, entries), nil
}

func (r *PeriodSeriesSnapshotRepository) CleanupReportedBefore(ctx context.Context, before time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("period series repository is not initialized")
	}
	if before.IsZero() {
		return 0, fmt.Errorf("period series cleanup cutoff is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxPeriodSeriesSnapshotCleanupPeriods {
		limit = maxPeriodSeriesSnapshotCleanupPeriods
	}
	cutoff := before.UTC()
	// The retention cutoff is deliberately supplied by bootstrap (30 days) so
	// period series snapshots outlive SCF callbacks, retries, and upstream recovery. A
	// direct Storage period has no Collector readiness row; for those periods,
	// the age cutoff plus absence of in-flight work is the terminal evidence.
	// Keep any period with unreported readiness or retry failure state.
	result := r.db.WithContext(ctx).Exec(`
DELETE FROM t_collector_task_period_series
WHERE (c_space_id, c_dataset_id, c_frequency, c_period_time) IN (
    SELECT candidates.c_space_id, candidates.c_dataset_id, candidates.c_frequency, candidates.c_period_time
    FROM t_collector_task_period_series AS candidates
    WHERE candidates.c_period_time < ?
      AND (
          NOT EXISTS (
              SELECT 1 FROM t_period_readiness AS readiness
              WHERE readiness.c_space_id = candidates.c_space_id
                AND readiness.c_dataset_id = candidates.c_dataset_id
                AND readiness.c_frequency = candidates.c_frequency
                AND readiness.c_period_time = candidates.c_period_time
          )
          OR EXISTS (
              SELECT 1 FROM t_period_readiness AS readiness
              WHERE readiness.c_space_id = candidates.c_space_id
                AND readiness.c_dataset_id = candidates.c_dataset_id
                AND readiness.c_frequency = candidates.c_frequency
                AND readiness.c_period_time = candidates.c_period_time
                AND readiness.c_status IN (?, ?)
                AND readiness.c_report_state = ?
                AND readiness.c_collected_at IS NOT NULL
                AND readiness.c_collected_at < ?
          )
      )
      AND NOT EXISTS (
          SELECT 1
          FROM t_collector_fetch_retry_items AS retries
          JOIN t_collector_task_period_series AS snapshot
            ON snapshot.c_space_id = retries.c_space_id
           AND snapshot.c_frequency = retries.c_frequency
           AND snapshot.c_period_time = retries.c_target_data_time
           AND snapshot.c_subject_id = retries.c_subject_id
          WHERE snapshot.c_space_id = candidates.c_space_id
            AND snapshot.c_dataset_id = candidates.c_dataset_id
            AND snapshot.c_frequency = candidates.c_frequency
            AND snapshot.c_period_time = candidates.c_period_time
            AND (
                retries.c_status IN ('pending', 'dispatched')
                OR (retries.c_status = 'permanent_failed' AND retries.c_period_failure_reported = 0)
            )
      )
      AND NOT EXISTS (
          SELECT 1
          FROM t_collector_fetch_batches AS batches
          JOIN t_collector_fetch_batch_items AS batch_items
            ON batch_items.c_space_id = batches.c_space_id AND batch_items.c_batch_id = batches.c_batch_id
          JOIN t_collector_task_instances AS instances
            ON instances.c_space_id = batch_items.c_space_id AND instances.c_instance_id = batch_items.c_instance_id
          JOIN t_collector_task_period_series AS snapshot
            ON snapshot.c_space_id = instances.c_space_id
           AND snapshot.c_frequency = instances.c_frequency
           AND snapshot.c_period_time = instances.c_target_data_time
           AND snapshot.c_subject_id = instances.c_subject_id
          WHERE snapshot.c_space_id = candidates.c_space_id
            AND snapshot.c_dataset_id = candidates.c_dataset_id
            AND snapshot.c_frequency = candidates.c_frequency
            AND snapshot.c_period_time = candidates.c_period_time
            AND batches.c_status IN ('planned', 'dispatched')
      )
    GROUP BY candidates.c_space_id, candidates.c_dataset_id, candidates.c_frequency, candidates.c_period_time
    ORDER BY MIN(candidates.c_period_time), MIN(candidates.c_ctime)
    LIMIT ?
)`, cutoff, domain.PeriodStatusComplete, domain.PeriodStatusDegraded, domain.PeriodReportReported, cutoff, limit)
	return result.RowsAffected, result.Error
}

func loadPeriodSeriesSnapshotEntries(tx *gorm.DB, spaceID, datasetID, frequency string, period time.Time) ([]domain.PeriodSeriesSnapshotEntry, error) {
	var rows []domain.PeriodSeriesSnapshotEntry
	err := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", spaceID, datasetID, frequency, period.UTC()).
		Order("c_series_index ASC").Find(&rows).Error
	return rows, err
}

func normalizePeriodSeriesSnapshotKey(key domain.PeriodKey) (domain.PeriodKey, error) {
	key.SpaceID = strings.TrimSpace(key.SpaceID)
	key.DatasetID = strings.TrimSpace(key.DatasetID)
	key.Frequency = strings.TrimSpace(key.Frequency)
	if _, err := marketdata.ParseFrequency(key.Frequency); err != nil {
		return domain.PeriodKey{}, fmt.Errorf("invalid period frequency %q: %w", key.Frequency, err)
	}
	if key.SpaceID == "" || key.DatasetID == "" || key.Frequency == "" || key.PeriodTime.IsZero() {
		return domain.PeriodKey{}, fmt.Errorf("space_id, dataset_id, frequency and period_time are required")
	}
	key.PeriodTime = key.PeriodTime.UTC()
	return key, nil
}

func normalizeAndValidatePeriodSeriesSnapshotEntries(input []domain.PeriodSeriesSnapshotEntry) ([]domain.PeriodSeriesSnapshotEntry, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("period series snapshot must not be empty")
	}
	rows := append([]domain.PeriodSeriesSnapshotEntry(nil), input...)
	now := time.Now().UTC()
	for i := range rows {
		row := &rows[i]
		row.ID = 0
		row.SpaceID = strings.TrimSpace(row.SpaceID)
		row.DatasetID = strings.TrimSpace(row.DatasetID)
		row.Frequency = strings.TrimSpace(row.Frequency)
		if _, err := marketdata.ParseFrequency(row.Frequency); err != nil {
			return nil, fmt.Errorf("invalid period series frequency %q: %w", row.Frequency, err)
		}
		row.PeriodTime = row.PeriodTime.UTC()
		row.SeriesKey = strings.TrimSpace(row.SeriesKey)
		row.SubjectID = strings.ToUpper(strings.TrimSpace(row.SubjectID))
		row.Provider = strings.ToLower(strings.TrimSpace(row.Provider))
		row.SourceID = strings.ToLower(strings.TrimSpace(row.SourceID))
		row.MarketType = strings.ToLower(strings.TrimSpace(row.MarketType))
		row.ProviderSymbol = strings.TrimSpace(row.ProviderSymbol)
		row.SeriesTag = strings.TrimSpace(row.SeriesTag)
		row.SeriesHash = strings.ToLower(strings.TrimSpace(row.SeriesHash))
		row.CreateTime = now
		if row.SpaceID == "" || row.DatasetID == "" || row.Frequency == "" || row.PeriodTime.IsZero() {
			return nil, fmt.Errorf("space_id, dataset_id, frequency and period_time are required")
		}
		if row.SubjectID == "" || row.Provider == "" || row.SourceID == "" || row.MarketType == "" || row.ProviderSymbol == "" {
			return nil, fmt.Errorf("subject_id, provider, source_id, market_type and provider_symbol are required")
		}
		canonicalKey := domain.CanonicalSeriesKey(row.Provider, row.SourceID, row.MarketType, row.SubjectID, row.SeriesTag)
		if row.SeriesKey == "" || row.SeriesKey != canonicalKey {
			return nil, fmt.Errorf("series_key does not match canonical series identity")
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SeriesIndex < rows[j].SeriesIndex })
	identity := rows[0]
	keys := make([]string, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i := range rows {
		row := rows[i]
		if row.SpaceID != identity.SpaceID || row.DatasetID != identity.DatasetID || row.Frequency != identity.Frequency || !row.PeriodTime.Equal(identity.PeriodTime) {
			return nil, fmt.Errorf("period series rows must share one period identity")
		}
		if row.SeriesIndex != uint32(i) {
			return nil, fmt.Errorf("period series indices must be dense from zero")
		}
		if row.ExpectedCount != len(rows) {
			return nil, fmt.Errorf("period series expected_count must equal snapshot size")
		}
		if _, exists := seen[row.SeriesKey]; exists {
			return nil, fmt.Errorf("period series keys must be unique")
		}
		seen[row.SeriesKey] = struct{}{}
		keys[i] = row.SeriesKey
	}
	sortedKeys := append([]string(nil), keys...)
	sort.Strings(sortedKeys)
	for i := range keys {
		if keys[i] != sortedKeys[i] {
			return nil, fmt.Errorf("period series indices must follow sorted series keys")
		}
	}
	hash := domain.SeriesSetHash(sortedKeys)
	for i := range rows {
		if rows[i].SeriesHash != hash {
			return nil, fmt.Errorf("period series hash does not match canonical snapshot")
		}
	}
	return rows, nil
}

func samePeriodSeriesSnapshotEntries(left, right []domain.PeriodSeriesSnapshotEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.SpaceID != b.SpaceID || a.DatasetID != b.DatasetID || a.Frequency != b.Frequency || !a.PeriodTime.Equal(b.PeriodTime) ||
			a.SeriesIndex != b.SeriesIndex || a.SeriesKey != b.SeriesKey || a.SubjectID != b.SubjectID || a.Provider != b.Provider ||
			a.SourceID != b.SourceID || a.MarketType != b.MarketType || a.ProviderSymbol != b.ProviderSymbol || a.SeriesTag != b.SeriesTag ||
			a.SeriesHash != b.SeriesHash || a.ExpectedCount != b.ExpectedCount {
			return false
		}
	}
	return true
}
