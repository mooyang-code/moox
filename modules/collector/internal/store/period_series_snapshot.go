package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxPeriodSeriesSnapshotCleanupCandidates = 1000

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
	db                 *gorm.DB
	cleanupMu          sync.Mutex
	cleanupCursorSpace string
	cleanupCursor      *PeriodSeriesSnapshotCursor
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

// ListCleanupCandidates returns old-period snapshots without a recently
// confirmed terminal Storage observation in stable per-space keyset order.
// The cursor points to the last returned key between reconciliation rounds.
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
	if limit > maxPeriodSeriesSnapshotCleanupCandidates {
		limit = maxPeriodSeriesSnapshotCleanupCandidates
	}
	query := `SELECT DISTINCT snapshots.c_space_id, snapshots.c_dataset_id, snapshots.c_frequency, snapshots.c_period_time,
       COALESCE(terminal_state.c_series_hash, snapshots.c_series_hash) AS c_series_hash,
       COALESCE(terminal_state.c_expected_count, snapshots.c_expected_count) AS c_expected_count
FROM t_collector_task_period_series AS snapshots
LEFT JOIN t_collector_period_storage_states AS terminal_state
  ON terminal_state.c_space_id = snapshots.c_space_id
 AND terminal_state.c_dataset_id = snapshots.c_dataset_id
 AND terminal_state.c_frequency = snapshots.c_frequency
 AND terminal_state.c_period_time = snapshots.c_period_time
 AND terminal_state.c_series_hash = snapshots.c_series_hash
 AND terminal_state.c_expected_count = snapshots.c_expected_count
 AND terminal_state.c_status IN (?, ?)
WHERE snapshots.c_space_id = ? AND snapshots.c_period_time < ?
  AND NOT EXISTS (
      SELECT 1 FROM t_collector_period_storage_states AS recent_state
      WHERE recent_state.c_space_id = snapshots.c_space_id
        AND recent_state.c_dataset_id = snapshots.c_dataset_id
        AND recent_state.c_frequency = snapshots.c_frequency
        AND recent_state.c_period_time = snapshots.c_period_time
        AND recent_state.c_series_hash = snapshots.c_series_hash
        AND recent_state.c_expected_count = snapshots.c_expected_count
        AND recent_state.c_status IN (?, ?)
        AND recent_state.c_confirmed_at >= ?
  )`
	args := []any{domain.PeriodStatusComplete, domain.PeriodStatusDegraded, spaceID, before.UTC(), domain.PeriodStatusComplete, domain.PeriodStatusDegraded, before.UTC()}
	if cursor != nil {
		query += ` AND (snapshots.c_period_time > ? OR (snapshots.c_period_time = ? AND snapshots.c_dataset_id > ?) OR (snapshots.c_period_time = ? AND snapshots.c_dataset_id = ? AND snapshots.c_frequency > ?))`
		args = append(args, cursor.PeriodTime.UTC(), cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.Frequency)
	}
	query += ` ORDER BY snapshots.c_period_time, snapshots.c_dataset_id, snapshots.c_frequency LIMIT ?`
	args = append(args, limit)
	var keys []periodSeriesSnapshotKeyRow
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&keys).Error; err != nil {
		return PeriodSeriesSnapshotPage{}, err
	}
	page := PeriodSeriesSnapshotPage{Snapshots: make([]domain.PeriodSeriesSnapshot, 0, len(keys))}
	for _, key := range keys {
		periodKey := domain.PeriodKey{SpaceID: key.SpaceID, DatasetID: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime}
		entries, err := loadPeriodSeriesSnapshotEntries(r.db.WithContext(ctx), periodKey.SpaceID, periodKey.DatasetID, periodKey.Frequency, periodKey.PeriodTime)
		if err != nil {
			return PeriodSeriesSnapshotPage{}, err
		}
		if len(entries) == 0 {
			continue
		}
		// Partial cleanup leaves a suffix-sized snapshot under its old terminal
		// Storage state, so probe using that persisted identity without asking a
		// full-snapshot validator to treat the cleanup progress as a new roster.
		snapshot := domain.PeriodSeriesSnapshot{Key: periodKey, SeriesHash: key.SeriesHash, ExpectedCount: key.ExpectedCount, Entries: entries}
		page.Snapshots = append(page.Snapshots, snapshot)
	}
	if len(page.Snapshots) > 0 {
		last := page.Snapshots[len(page.Snapshots)-1].Key
		page.Next = &PeriodSeriesSnapshotCursor{PeriodTime: last.PeriodTime, DatasetID: last.DatasetID, Frequency: last.Frequency}
	}
	return page, nil
}

type periodSeriesSnapshotKeyRow struct {
	SpaceID       string    `gorm:"column:c_space_id"`
	DatasetID     string    `gorm:"column:c_dataset_id"`
	Frequency     string    `gorm:"column:c_frequency"`
	PeriodTime    time.Time `gorm:"column:c_period_time"`
	SeriesHash    string    `gorm:"column:c_series_hash"`
	ExpectedCount uint32    `gorm:"column:c_expected_count"`
}

// CleanupTerminalBefore incrementally removes old terminal snapshot rows under
// a physical-row budget. The Storage state is retained as progress until the
// last series row is gone; Timer manifests are left to their separately
// budgeted cleanup path.
func (r *PeriodSeriesSnapshotRepository) CleanupTerminalBefore(ctx context.Context, spaceID string, before time.Time, limit int) (int64, error) {
	counts, err := r.CleanupTerminalBeforeWithCounts(ctx, spaceID, before, limit)
	return counts.Total(), err
}

type PeriodCleanupCounts struct {
	SnapshotRows int64
	StateRows    int64
	ManifestRows int64
}

func (c PeriodCleanupCounts) Total() int64 { return c.SnapshotRows + c.StateRows + c.ManifestRows }

// CleanupTerminalBeforeWithCounts distinguishes committed series-entry and
// Storage-state deletes. Both consume the same physical-row limit.
func (r *PeriodSeriesSnapshotRepository) CleanupTerminalBeforeWithCounts(ctx context.Context, spaceID string, before time.Time, limit int) (PeriodCleanupCounts, error) {
	if r == nil || r.db == nil {
		return PeriodCleanupCounts{}, fmt.Errorf("period series repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" || before.IsZero() {
		return PeriodCleanupCounts{}, fmt.Errorf("space_id and period series cleanup cutoff are required")
	}
	if limit <= 0 {
		limit = 100
	}
	r.cleanupMu.Lock()
	defer r.cleanupMu.Unlock()
	cursor := r.cleanupCursor
	if r.cleanupCursorSpace != spaceID {
		cursor = nil
	}
	deletedRows := int64(0)
	stateRows := int64(0)
	wrapped := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for deletedRows < int64(limit) {
			candidates, err := listTerminalPeriodCleanupCandidates(tx, spaceID, before.UTC(), cursor, maxPeriodSeriesSnapshotCleanupCandidates)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				if cursor != nil && !wrapped {
					cursor = nil
					wrapped = true
					continue
				}
				cursor = nil
				break
			}
			processed := 0
			for _, candidate := range candidates {
				key := candidate.Key
				cursor = &PeriodSeriesSnapshotCursor{PeriodTime: key.PeriodTime, DatasetID: key.DatasetID, Frequency: key.Frequency}
				removed, err := cleanupTerminalPeriodChunk(tx, candidate, before.UTC(), int64(limit)-deletedRows, &stateRows)
				if err != nil {
					return err
				}
				deletedRows += removed
				processed++
				if deletedRows >= int64(limit) {
					break
				}
			}
			if processed < len(candidates) {
				break
			}
			if len(candidates) < maxPeriodSeriesSnapshotCleanupCandidates {
				cursor = nil
				break
			}
		}
		return nil
	})
	if err != nil {
		return PeriodCleanupCounts{}, err
	}
	r.cleanupCursorSpace = spaceID
	r.cleanupCursor = cursor
	return PeriodCleanupCounts{SnapshotRows: deletedRows - stateRows, StateRows: stateRows}, nil
}

type periodSeriesSnapshotCleanupCandidate struct {
	Key           domain.PeriodKey
	SeriesHash    string
	ExpectedCount uint32
}

func listTerminalPeriodCleanupCandidates(tx *gorm.DB, spaceID string, before time.Time, cursor *PeriodSeriesSnapshotCursor, limit int) ([]periodSeriesSnapshotCleanupCandidate, error) {
	query := `SELECT states.c_space_id, states.c_dataset_id, states.c_frequency, states.c_period_time,
       states.c_series_hash, states.c_expected_count
FROM t_collector_period_storage_states AS states
LEFT JOIN t_collector_task_period_series AS snapshots
  ON snapshots.c_space_id = states.c_space_id
 AND snapshots.c_dataset_id = states.c_dataset_id
 AND snapshots.c_frequency = states.c_frequency
 AND snapshots.c_period_time = states.c_period_time
WHERE states.c_space_id = ? AND states.c_confirmed_at < ? AND states.c_status IN (?, ?)
  AND ` + periodCleanupNoActiveReferencesSQL("states")
	args := []any{spaceID, before, domain.PeriodStatusComplete, domain.PeriodStatusDegraded}
	if cursor != nil {
		query += ` AND (states.c_period_time > ? OR (states.c_period_time = ? AND states.c_dataset_id > ?) OR (states.c_period_time = ? AND states.c_dataset_id = ? AND states.c_frequency > ?))`
		args = append(args, cursor.PeriodTime.UTC(), cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.PeriodTime.UTC(), cursor.DatasetID, cursor.Frequency)
	}
	query += `
GROUP BY states.c_space_id, states.c_dataset_id, states.c_frequency, states.c_period_time,
         states.c_series_hash, states.c_expected_count
HAVING COUNT(snapshots.c_id) = 0 OR (
    COUNT(snapshots.c_id) = SUM(CASE WHEN snapshots.c_ctime < ? THEN 1 ELSE 0 END)
    AND COUNT(snapshots.c_id) = SUM(CASE WHEN snapshots.c_series_hash = states.c_series_hash AND snapshots.c_expected_count = states.c_expected_count THEN 1 ELSE 0 END)
  )`
	args = append(args, before)
	query += ` ORDER BY states.c_period_time, states.c_dataset_id, states.c_frequency LIMIT ?`
	args = append(args, limit)
	var rows []periodSeriesSnapshotKeyRow
	if err := tx.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	candidates := make([]periodSeriesSnapshotCleanupCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, periodSeriesSnapshotCleanupCandidate{
			Key:           domain.PeriodKey{SpaceID: row.SpaceID, DatasetID: row.DatasetID, Frequency: row.Frequency, PeriodTime: row.PeriodTime.UTC()},
			SeriesHash:    row.SeriesHash,
			ExpectedCount: row.ExpectedCount,
		})
	}
	return candidates, nil
}

func periodCleanupNoActiveReferencesSQL(stateAlias string) string {
	return fmt.Sprintf(`NOT EXISTS (
    SELECT 1 FROM t_collector_fetch_retry_items AS retries
    WHERE retries.c_space_id = %[1]s.c_space_id AND retries.c_frequency = %[1]s.c_frequency
      AND (retries.c_period_time = %[1]s.c_period_time OR retries.c_target_data_time = %[1]s.c_period_time)
      AND (retries.c_status IN ('pending', 'dispatched') OR (retries.c_status = 'permanent_failed' AND retries.c_period_failure_report_state = 'pending'))
  )
  AND NOT EXISTS (
    SELECT 1
    FROM t_collector_fetch_batches AS batches
    JOIN t_collector_fetch_batch_items AS batch_items
      ON batch_items.c_space_id = batches.c_space_id AND batch_items.c_batch_id = batches.c_batch_id
    JOIN t_collector_task_instances AS instances
      ON instances.c_space_id = batch_items.c_space_id AND instances.c_instance_id = batch_items.c_instance_id
    WHERE batches.c_space_id = %[1]s.c_space_id AND batches.c_status IN ('planned', 'dispatched')
      AND instances.c_frequency = %[1]s.c_frequency AND instances.c_target_data_time = %[1]s.c_period_time
  )
  AND NOT EXISTS (
    SELECT 1 FROM t_collector_timer_period_batches AS manifests
    WHERE manifests.c_space_id = %[1]s.c_space_id AND manifests.c_dataset_id = %[1]s.c_dataset_id
      AND manifests.c_frequency = %[1]s.c_frequency AND manifests.c_period_time = %[1]s.c_period_time
  )
  AND NOT EXISTS (
    SELECT 1
    FROM t_collector_instance_write_targets AS targets
    JOIN t_collector_task_instances AS instances
      ON instances.c_space_id = targets.c_space_id AND instances.c_instance_id = targets.c_instance_id
    WHERE targets.c_space_id = %[1]s.c_space_id AND targets.c_dataset_id = %[1]s.c_dataset_id
      AND instances.c_data_type = 'kline' AND instances.c_frequency = %[1]s.c_frequency
      AND instances.c_target_data_time = %[1]s.c_period_time
  )`, stateAlias)
}

func cleanupTerminalPeriodChunk(tx *gorm.DB, candidate periodSeriesSnapshotCleanupCandidate, before time.Time, budget int64, statesDeleted *int64) (int64, error) {
	if budget <= 0 {
		return 0, nil
	}
	key := candidate.Key
	entries, err := loadPeriodSeriesSnapshotEntries(tx, key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime)
	if err != nil {
		return 0, err
	}
	if err := validateTerminalPeriodCleanupEntries(candidate, entries, before); err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		rows, err := deleteTerminalPeriodStorageState(tx, candidate, before)
		if err == nil {
			*statesDeleted += rows
		}
		return rows, err
	}
	if uint64(len(entries))+1 <= uint64(budget) {
		result := tx.Exec(`DELETE FROM t_collector_task_period_series AS snapshots
WHERE snapshots.c_space_id = ? AND snapshots.c_dataset_id = ? AND snapshots.c_frequency = ? AND snapshots.c_period_time = ?
  AND snapshots.c_series_hash = ? AND snapshots.c_expected_count = ? AND snapshots.c_ctime < ?
  AND EXISTS (
    SELECT 1 FROM t_collector_period_storage_states AS state
    WHERE state.c_space_id = ? AND state.c_dataset_id = ? AND state.c_frequency = ? AND state.c_period_time = ?
      AND state.c_series_hash = ? AND state.c_expected_count = ? AND state.c_status IN (?, ?) AND state.c_confirmed_at < ?
      AND `+periodCleanupNoActiveReferencesSQL("state")+`
  )`,
			key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, candidate.SeriesHash, candidate.ExpectedCount, before,
			key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, candidate.SeriesHash, candidate.ExpectedCount,
			domain.PeriodStatusComplete, domain.PeriodStatusDegraded, before,
		)
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected == 0 {
			return 0, nil
		}
		if result.RowsAffected != int64(len(entries)) {
			return 0, fmt.Errorf("period snapshot cleanup deleted %d rows, expected %d", result.RowsAffected, len(entries))
		}
		stateRows, err := deleteTerminalPeriodStorageState(tx, candidate, before)
		if err != nil {
			return 0, err
		}
		if stateRows != 1 {
			return 0, fmt.Errorf("period snapshot cleanup could not remove confirmed Storage state")
		}
		*statesDeleted += stateRows
		return result.RowsAffected + stateRows, nil
	}
	rowsToDelete := min(int64(len(entries)), budget)
	result := tx.Exec(`DELETE FROM t_collector_task_period_series AS snapshots
WHERE snapshots.c_id IN (
    SELECT c_id FROM t_collector_task_period_series
    WHERE c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?
      AND c_series_hash = ? AND c_expected_count = ? AND c_ctime < ?
    ORDER BY c_series_index DESC LIMIT ?
  )
  AND EXISTS (
    SELECT 1 FROM t_collector_period_storage_states AS state
    WHERE state.c_space_id = ? AND state.c_dataset_id = ? AND state.c_frequency = ? AND state.c_period_time = ?
      AND state.c_series_hash = ? AND state.c_expected_count = ? AND state.c_status IN (?, ?) AND state.c_confirmed_at < ?
      AND `+periodCleanupNoActiveReferencesSQL("state")+`
  )`,
		key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, candidate.SeriesHash, candidate.ExpectedCount, before, rowsToDelete,
		key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, candidate.SeriesHash, candidate.ExpectedCount,
		domain.PeriodStatusComplete, domain.PeriodStatusDegraded, before,
	)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected > rowsToDelete {
		return 0, fmt.Errorf("period snapshot cleanup deleted %d rows with a chunk budget of %d", result.RowsAffected, rowsToDelete)
	}
	return result.RowsAffected, nil
}

func deleteTerminalPeriodStorageState(tx *gorm.DB, candidate periodSeriesSnapshotCleanupCandidate, before time.Time) (int64, error) {
	key := candidate.Key
	result := tx.Exec(`DELETE FROM t_collector_period_storage_states AS states
WHERE states.c_space_id = ? AND states.c_dataset_id = ? AND states.c_frequency = ? AND states.c_period_time = ?
  AND states.c_series_hash = ? AND states.c_expected_count = ? AND states.c_status IN (?, ?) AND states.c_confirmed_at < ?
  AND NOT EXISTS (
    SELECT 1 FROM t_collector_task_period_series AS snapshots
    WHERE snapshots.c_space_id = states.c_space_id AND snapshots.c_dataset_id = states.c_dataset_id
      AND snapshots.c_frequency = states.c_frequency AND snapshots.c_period_time = states.c_period_time
  )
  AND `+periodCleanupNoActiveReferencesSQL("states"),
		key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime, candidate.SeriesHash, candidate.ExpectedCount,
		domain.PeriodStatusComplete, domain.PeriodStatusDegraded, before,
	)
	return result.RowsAffected, result.Error
}

func validateTerminalPeriodCleanupEntries(candidate periodSeriesSnapshotCleanupCandidate, entries []domain.PeriodSeriesSnapshotEntry, before time.Time) error {
	key := candidate.Key
	if uint64(len(entries)) > uint64(candidate.ExpectedCount) {
		return fmt.Errorf("period snapshot %s/%s/%s has more rows than its terminal Storage identity", key.SpaceID, key.DatasetID, key.Frequency)
	}
	for index, entry := range entries {
		if entry.SpaceID != key.SpaceID || entry.DatasetID != key.DatasetID || entry.Frequency != key.Frequency || !entry.PeriodTime.Equal(key.PeriodTime) ||
			entry.SeriesHash != candidate.SeriesHash || entry.ExpectedCount != int(candidate.ExpectedCount) || entry.SeriesIndex != uint32(index) || !entry.CreateTime.Before(before) {
			return fmt.Errorf("period snapshot %s/%s/%s cleanup progress does not match its terminal Storage identity", key.SpaceID, key.DatasetID, key.Frequency)
		}
		if entry.SeriesKey != domain.CanonicalSeriesKey(entry.Provider, entry.SourceID, entry.MarketType, entry.SubjectID, entry.SeriesTag) {
			return fmt.Errorf("period snapshot %s/%s/%s cleanup found a non-canonical series identity", key.SpaceID, key.DatasetID, key.Frequency)
		}
	}
	return nil
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
	frequency, err := marketdata.ParseFrequency(key.Frequency)
	if err != nil {
		return domain.PeriodKey{}, fmt.Errorf("invalid period frequency %q: %w", key.Frequency, err)
	}
	key.Frequency = string(frequency)
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
		frequency, err := marketdata.ParseFrequency(row.Frequency)
		if err != nil {
			return nil, fmt.Errorf("invalid period series frequency %q: %w", row.Frequency, err)
		}
		row.Frequency = string(frequency)
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
