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

type PeriodSeriesSnapshotCursor struct {
	PeriodTime time.Time
	DatasetID  string
	Frequency  string
}

type PeriodSeriesSnapshotPage struct {
	Snapshots []domain.PeriodSeriesSnapshot
	Next      *PeriodSeriesSnapshotCursor
}

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

// ListCleanupCandidates returns expired snapshots in stable per-space keyset
// order. The cursor points to the last returned key and can be retained in
// memory by a reconciler between rounds.
func (r *PeriodSeriesSnapshotRepository) ListCleanupCandidates(ctx context.Context, spaceID string, before time.Time, cursor *PeriodSeriesSnapshotCursor, limit int) (PeriodSeriesSnapshotPage, error) {
	if r == nil || r.db == nil {
		return PeriodSeriesSnapshotPage{}, fmt.Errorf("period series repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || before.IsZero() {
		return PeriodSeriesSnapshotPage{}, fmt.Errorf("space_id and period series cleanup cutoff are required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxPeriodSeriesSnapshotCleanupPeriods {
		limit = maxPeriodSeriesSnapshotCleanupPeriods
	}
	query := `SELECT DISTINCT c_space_id, c_dataset_id, c_frequency, c_period_time
FROM t_collector_task_period_series
WHERE c_space_id = ? AND c_period_time < ?`
	args := []any{spaceID, before.UTC()}
	if cursor != nil {
		query += ` AND (c_period_time > ? OR (c_period_time = ? AND c_dataset_id > ?) OR (c_period_time = ? AND c_dataset_id = ? AND c_frequency > ?))`
		args = append(args, cursor.PeriodTime.UTC(), cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.Frequency)
	}
	query += ` ORDER BY c_period_time, c_dataset_id, c_frequency LIMIT ?`
	args = append(args, limit)
	var keys []periodSeriesSnapshotKeyRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&keys).Error; err != nil {
		return PeriodSeriesSnapshotPage{}, err
	}
	page := PeriodSeriesSnapshotPage{Snapshots: make([]domain.PeriodSeriesSnapshot, 0, len(keys))}
	for _, key := range keys {
		snapshot, found, err := r.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime})
		if err != nil {
			return PeriodSeriesSnapshotPage{}, err
		}
		if !found {
			continue
		}
		page.Snapshots = append(page.Snapshots, snapshot)
	}
	if len(page.Snapshots) > 0 {
		last := page.Snapshots[len(page.Snapshots)-1].Key
		page.Next = &PeriodSeriesSnapshotCursor{PeriodTime: last.PeriodTime, DatasetID: last.DatasetID, Frequency: last.Frequency}
	}
	return page, nil
}

type periodSeriesSnapshotKeyRow struct {
	SpaceID    string    `gorm:"column:c_space_id"`
	DatasetID  string    `gorm:"column:c_dataset_id"`
	Frequency  string    `gorm:"column:c_frequency"`
	PeriodTime time.Time `gorm:"column:c_period_time"`
}

// CleanupTerminalBefore deletes whole old periods only when Storage has
// confirmed a matching complete/degraded state and no Collector work remains.
// limit and the returned count are measured in periods, not snapshot rows.
func (r *PeriodSeriesSnapshotRepository) CleanupTerminalBefore(ctx context.Context, spaceID string, before time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("period series repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || before.IsZero() {
		return 0, fmt.Errorf("space_id and period series cleanup cutoff are required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxPeriodSeriesSnapshotCleanupPeriods {
		limit = maxPeriodSeriesSnapshotCleanupPeriods
	}
	deletedPeriods := int64(0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []periodSeriesSnapshotKeyRow
		err := tx.Raw(`
SELECT snapshots.c_space_id, snapshots.c_dataset_id, snapshots.c_frequency, snapshots.c_period_time
FROM t_collector_task_period_series AS snapshots
JOIN t_collector_period_storage_states AS storage_state
  ON storage_state.c_space_id = snapshots.c_space_id
 AND storage_state.c_dataset_id = snapshots.c_dataset_id
 AND storage_state.c_frequency = snapshots.c_frequency
 AND storage_state.c_period_time = snapshots.c_period_time
 AND storage_state.c_series_hash = snapshots.c_series_hash
 AND storage_state.c_expected_count = snapshots.c_expected_count
WHERE snapshots.c_space_id = ? AND snapshots.c_period_time < ?
  AND storage_state.c_status IN (?, ?)
  AND NOT EXISTS (
      SELECT 1
      FROM t_collector_fetch_retry_items AS retries
      JOIN t_collector_task_period_series AS retry_snapshot
        ON retry_snapshot.c_space_id = retries.c_space_id
       AND retry_snapshot.c_frequency = retries.c_frequency
       AND retry_snapshot.c_period_time = retries.c_target_data_time
       AND retry_snapshot.c_subject_id = retries.c_subject_id
      WHERE retry_snapshot.c_space_id = snapshots.c_space_id
        AND retry_snapshot.c_dataset_id = snapshots.c_dataset_id
        AND retry_snapshot.c_frequency = snapshots.c_frequency
        AND retry_snapshot.c_period_time = snapshots.c_period_time
        AND (retries.c_status IN ('pending', 'dispatched') OR (retries.c_status = 'permanent_failed' AND retries.c_period_failure_reported = 0))
  )
  AND NOT EXISTS (
      SELECT 1
      FROM t_collector_fetch_batches AS batches
      JOIN t_collector_fetch_batch_items AS batch_items
        ON batch_items.c_space_id = batches.c_space_id AND batch_items.c_batch_id = batches.c_batch_id
      JOIN t_collector_task_instances AS instances
        ON instances.c_space_id = batch_items.c_space_id AND instances.c_instance_id = batch_items.c_instance_id
      JOIN t_collector_task_period_series AS batch_snapshot
        ON batch_snapshot.c_space_id = instances.c_space_id
       AND batch_snapshot.c_frequency = instances.c_frequency
       AND batch_snapshot.c_period_time = instances.c_target_data_time
       AND batch_snapshot.c_subject_id = instances.c_subject_id
      WHERE batch_snapshot.c_space_id = snapshots.c_space_id
        AND batch_snapshot.c_dataset_id = snapshots.c_dataset_id
        AND batch_snapshot.c_frequency = snapshots.c_frequency
        AND batch_snapshot.c_period_time = snapshots.c_period_time
        AND batches.c_status IN ('planned', 'dispatched')
  )
GROUP BY snapshots.c_space_id, snapshots.c_dataset_id, snapshots.c_frequency, snapshots.c_period_time,
         storage_state.c_series_hash, storage_state.c_expected_count
HAVING COUNT(*) = storage_state.c_expected_count
ORDER BY snapshots.c_period_time, snapshots.c_dataset_id, snapshots.c_frequency
LIMIT ?`, spaceID, before.UTC(), domain.PeriodStatusComplete, domain.PeriodStatusDegraded, limit).Scan(&candidates).Error
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			key := domain.PeriodKey{SpaceID: candidate.SpaceID, DatasetID: candidate.DatasetID, Frequency: candidate.Frequency, PeriodTime: candidate.PeriodTime}
			entries, err := loadPeriodSeriesSnapshotEntries(tx, key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				continue
			}
			entries, err = normalizeAndValidatePeriodSeriesSnapshotEntries(entries)
			if err != nil {
				return fmt.Errorf("stored period series snapshot is invalid: %w", err)
			}
			snapshot := periodSeriesSnapshotFromEntries(key, entries)
			if int(snapshot.ExpectedCount) != len(snapshot.Entries) {
				return fmt.Errorf("period snapshot %s/%s/%s has inconsistent count", key.SpaceID, key.DatasetID, key.Frequency)
			}
			result := tx.Exec(`
DELETE FROM t_collector_task_period_series
WHERE c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?
  AND c_series_hash = ? AND c_expected_count = ?
  AND EXISTS (
      SELECT 1 FROM t_collector_period_storage_states AS state
      WHERE state.c_space_id = ? AND state.c_dataset_id = ? AND state.c_frequency = ? AND state.c_period_time = ?
        AND state.c_series_hash = ? AND state.c_expected_count = ? AND state.c_status IN (?, ?)
  )
  AND NOT EXISTS (
      SELECT 1
      FROM t_collector_fetch_retry_items AS retries
      JOIN t_collector_task_period_series AS retry_snapshot
        ON retry_snapshot.c_space_id = retries.c_space_id
       AND retry_snapshot.c_frequency = retries.c_frequency
       AND retry_snapshot.c_period_time = retries.c_target_data_time
       AND retry_snapshot.c_subject_id = retries.c_subject_id
      WHERE retry_snapshot.c_space_id = ? AND retry_snapshot.c_dataset_id = ? AND retry_snapshot.c_frequency = ? AND retry_snapshot.c_period_time = ?
        AND (retries.c_status IN ('pending', 'dispatched') OR (retries.c_status = 'permanent_failed' AND retries.c_period_failure_reported = 0))
  )
  AND NOT EXISTS (
      SELECT 1
      FROM t_collector_fetch_batches AS batches
      JOIN t_collector_fetch_batch_items AS batch_items
        ON batch_items.c_space_id = batches.c_space_id AND batch_items.c_batch_id = batches.c_batch_id
      JOIN t_collector_task_instances AS instances
        ON instances.c_space_id = batch_items.c_space_id AND instances.c_instance_id = batch_items.c_instance_id
      JOIN t_collector_task_period_series AS batch_snapshot
        ON batch_snapshot.c_space_id = instances.c_space_id
       AND batch_snapshot.c_frequency = instances.c_frequency
       AND batch_snapshot.c_period_time = instances.c_target_data_time
       AND batch_snapshot.c_subject_id = instances.c_subject_id
      WHERE batch_snapshot.c_space_id = ? AND batch_snapshot.c_dataset_id = ? AND batch_snapshot.c_frequency = ? AND batch_snapshot.c_period_time = ?
        AND batches.c_status IN ('planned', 'dispatched')
  )`,
				key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, snapshot.SeriesHash, snapshot.ExpectedCount,
				key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, snapshot.SeriesHash, snapshot.ExpectedCount, domain.PeriodStatusComplete, domain.PeriodStatusDegraded,
				key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime,
				key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime,
			)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if result.RowsAffected != int64(len(snapshot.Entries)) {
				return fmt.Errorf("period snapshot cleanup deleted %d rows, expected %d", result.RowsAffected, len(snapshot.Entries))
			}
			stateDelete := tx.Exec(`DELETE FROM t_collector_period_storage_states WHERE c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ? AND c_series_hash = ? AND c_expected_count = ? AND c_status IN (?, ?)`,
				key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, snapshot.SeriesHash, snapshot.ExpectedCount, domain.PeriodStatusComplete, domain.PeriodStatusDegraded)
			if stateDelete.Error != nil {
				return stateDelete.Error
			}
			if stateDelete.RowsAffected != 1 {
				return fmt.Errorf("period snapshot cleanup could not remove confirmed Storage state")
			}
			deletedPeriods++
		}
		return nil
	})
	return deletedPeriods, err
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
