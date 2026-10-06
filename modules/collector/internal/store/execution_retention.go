package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Scheduled execution details are swept with keyset windows over the
// (c_space_id, c_mtime, c_id) cleanup indexes. Each window evaluates at most
// MaxRetentionWindowRows candidates in its own short transaction and returns
// the cursor to resume from, so rows that must be kept (for example kline
// targets whose period is still waiting) are skipped once per sweep instead of
// being rescanned on every pass, and Collector's single SQLite connection is
// released between windows.
const MaxRetentionWindowRows = 5000

// RetentionCursor is the last (c_mtime, c_id) key a cleanup window examined.
// The zero value starts a sweep at the oldest row.
type RetentionCursor struct {
	MTime string
	ID    int64
}

func (c RetentionCursor) IsZero() bool { return c.MTime == "" && c.ID == 0 }

const scheduledWriteTargetWindowEndSQL = `SELECT CAST(c_mtime AS TEXT), c_id
	FROM t_collector_instance_write_targets INDEXED BY idx_collector_write_targets_retention_space
	WHERE c_space_id = ? AND c_mtime < ? AND (c_mtime, c_id) > (?, ?)
	ORDER BY c_mtime, c_id
	LIMIT 1 OFFSET ?`

const scheduledInstanceWindowEndSQL = `SELECT CAST(c_mtime AS TEXT), c_id
	FROM t_collector_task_instances INDEXED BY idx_collector_instances_terminal_cleanup_space
	WHERE c_space_id = ? AND c_run_id <> '' AND c_last_exec_status IN (2, 3)
	  AND c_mtime < ? AND (c_mtime, c_id) > (?, ?)
	ORDER BY c_mtime, c_id
	LIMIT 1 OFFSET ?`

const scheduledWriteTargetCleanupWindowSQL = `SELECT targets.c_id
	FROM t_collector_instance_write_targets AS targets INDEXED BY idx_collector_write_targets_retention_space
	JOIN t_collector_task_instances AS instances
	  ON instances.c_space_id = targets.c_space_id AND instances.c_instance_id = targets.c_instance_id
	JOIN t_collector_runs AS runs
	  ON runs.c_space_id = instances.c_space_id AND runs.c_run_id = instances.c_run_id
	WHERE targets.c_space_id = ?
	  AND targets.c_mtime < ?
	  AND (targets.c_mtime, targets.c_id) > (?, ?) %[1]s
	  AND targets.c_status IN ('succeeded', 'failed')
	  AND instances.c_run_id <> ''
	  AND instances.c_data_type <> 'kline_resample'
	  AND instances.c_mtime < ?
	  AND instances.c_last_exec_status IN (2, 3)
	  AND runs.c_run_type = 'scheduled'
	  AND runs.c_status IN ('succeeded', 'partial_failed', 'failed')
	  AND runs.c_mtime < ?
	  AND EXISTS (
	    SELECT 1
	    FROM t_collector_instance_write_targets AS newer_targets
	    JOIN t_collector_task_instances AS newer_instances INDEXED BY idx_collector_instances_storage_write
	      ON newer_instances.c_space_id = newer_targets.c_space_id AND newer_instances.c_instance_id = newer_targets.c_instance_id
	    JOIN t_collector_runs AS newer_runs
	      ON newer_runs.c_space_id = newer_instances.c_space_id AND newer_runs.c_run_id = newer_instances.c_run_id
	    WHERE newer_targets.c_space_id = targets.c_space_id
	      AND newer_targets.c_task_id = targets.c_task_id
	      AND newer_targets.c_dataset_id = targets.c_dataset_id
	      AND newer_targets.c_status = 'succeeded'
	      AND newer_instances.c_subject_id = instances.c_subject_id
	      AND newer_instances.c_frequency = instances.c_frequency
	      AND newer_instances.c_provider = instances.c_provider
	      AND newer_instances.c_source_id = instances.c_source_id
	      AND newer_instances.c_market_type = instances.c_market_type
	      AND newer_instances.c_series_tag = instances.c_series_tag
	      AND newer_instances.c_data_type = instances.c_data_type
	      AND newer_instances.c_id > instances.c_id
	      AND newer_instances.c_last_exec_status = 2
	      AND newer_runs.c_run_type = 'scheduled'
	      AND newer_runs.c_status IN ('succeeded', 'partial_failed', 'failed')
	      AND (
	        newer_instances.c_data_type <> 'kline'
	        OR EXISTS (
	          SELECT 1 FROM t_collector_period_storage_states AS newer_storage_states
	          WHERE newer_storage_states.c_space_id = newer_targets.c_space_id
	            AND newer_storage_states.c_dataset_id = newer_targets.c_dataset_id
	            AND newer_storage_states.c_frequency = newer_instances.c_frequency
	            AND newer_storage_states.c_period_time = newer_instances.c_target_data_time
	            AND newer_storage_states.c_status IN ('complete', 'degraded')
	        )
	      )
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_batches AS batches
	    WHERE batches.c_space_id = targets.c_space_id AND batches.c_write_target_id = targets.c_write_target_id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_batches AS batches
	    WHERE batches.c_space_id = instances.c_space_id AND batches.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM t_collector_fetch_batch_items AS items
	    WHERE items.c_space_id = instances.c_space_id
	      AND items.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_retry_items AS retries
	    WHERE retries.c_space_id = targets.c_space_id
	      AND retries.c_write_target_id = targets.c_write_target_id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_retry_items AS retries
	    WHERE retries.c_space_id = instances.c_space_id
	      AND retries.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM t_period_readiness_items AS items
	    JOIN t_period_readiness AS readiness ON readiness.c_id = items.c_readiness_id
	    WHERE items.c_write_target_id = targets.c_write_target_id
	      AND readiness.c_status = 'waiting'
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_period_storage_states AS storage_states
	    WHERE storage_states.c_space_id = targets.c_space_id
	      AND storage_states.c_dataset_id = targets.c_dataset_id
	      AND storage_states.c_frequency = instances.c_frequency
	      AND storage_states.c_period_time = instances.c_target_data_time
	      AND storage_states.c_status = 'waiting'
	  )
	  AND (
	    instances.c_data_type <> 'kline'
	    OR EXISTS (
	      SELECT 1 FROM t_collector_period_storage_states AS storage_states
	      WHERE storage_states.c_space_id = targets.c_space_id
	        AND storage_states.c_dataset_id = targets.c_dataset_id
	        AND storage_states.c_frequency = instances.c_frequency
	        AND storage_states.c_period_time = instances.c_target_data_time
	        AND storage_states.c_status IN ('complete', 'degraded')
	    )
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM t_collector_timer_period_batches AS manifests
	    LEFT JOIN t_collector_fetch_batches AS batches
	      ON batches.c_space_id = manifests.c_space_id AND batches.c_batch_id = manifests.c_batch_id
	    WHERE manifests.c_space_id = instances.c_space_id
	      AND manifests.c_first_run_id = instances.c_run_id
	      AND (batches.c_batch_id IS NULL OR batches.c_status NOT IN ('succeeded', 'partial_failed', 'failed', 'timed_out'))
	  )
`

const scheduledInstanceCleanupWindowSQL = `SELECT instances.c_id
	FROM t_collector_task_instances AS instances INDEXED BY idx_collector_instances_terminal_cleanup_space
	JOIN t_collector_runs AS runs
	  ON runs.c_space_id = instances.c_space_id AND runs.c_run_id = instances.c_run_id
	WHERE instances.c_space_id = ?
	  AND (instances.c_mtime, instances.c_id) > (?, ?) %[1]s
	  AND instances.c_run_id <> ''
	  AND instances.c_data_type <> 'kline_resample'
	  AND instances.c_mtime < ?
	  AND instances.c_last_exec_status IN (2, 3)
	  AND runs.c_run_type = 'scheduled'
	  AND runs.c_status IN ('succeeded', 'partial_failed', 'failed')
	  AND runs.c_mtime < ?
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_instance_write_targets AS targets
	    WHERE targets.c_space_id = instances.c_space_id AND targets.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_batches AS batches
	    WHERE batches.c_space_id = instances.c_space_id
	      AND batches.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM t_collector_fetch_batch_items AS items
	    WHERE items.c_space_id = instances.c_space_id
	      AND items.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1 FROM t_collector_fetch_retry_items AS retries
	    WHERE retries.c_space_id = instances.c_space_id
	      AND retries.c_instance_id = instances.c_instance_id
	  )
	  AND NOT EXISTS (
	    SELECT 1
	    FROM t_collector_timer_period_batches AS manifests
	    LEFT JOIN t_collector_fetch_batches AS batches
	      ON batches.c_space_id = manifests.c_space_id AND batches.c_batch_id = manifests.c_batch_id
	    WHERE manifests.c_space_id = instances.c_space_id
	      AND manifests.c_first_run_id = instances.c_run_id
	      AND (batches.c_batch_id IS NULL OR batches.c_status NOT IN ('succeeded', 'partial_failed', 'failed', 'timed_out'))
	  )
`

// CleanupScheduledWriteTargetsWindow expires old terminal write targets in the
// next window after `after`. A target is removed only after a newer successful
// sibling exists for the same task/Dataset/series, and never while resumable
// work still references it. The returned cursor is zero once the sweep has
// reached the cutoff.
func (r *TaskInstanceRepository) CleanupScheduledWriteTargetsWindow(ctx context.Context, spaceID string, cutoff time.Time, after RetentionCursor, window int) (int64, RetentionCursor, error) {
	return r.cleanupRetentionWindow(ctx, spaceID, cutoff, after, window, "t_collector_instance_write_targets",
		scheduledWriteTargetWindowEndSQL, scheduledWriteTargetCleanupWindowSQL, "targets",
		func(bounds []any) []any {
			return append(append([]any{spaceID, cutoff.UTC()}, bounds...), cutoff.UTC(), cutoff.UTC())
		})
}

// CleanupScheduledInstancesWindow expires old terminal scheduled instances in
// the next window after `after` once nothing references them any more.
func (r *TaskInstanceRepository) CleanupScheduledInstancesWindow(ctx context.Context, spaceID string, cutoff time.Time, after RetentionCursor, window int) (int64, RetentionCursor, error) {
	return r.cleanupRetentionWindow(ctx, spaceID, cutoff, after, window, "t_collector_task_instances",
		scheduledInstanceWindowEndSQL, scheduledInstanceCleanupWindowSQL, "instances",
		func(bounds []any) []any { return append(append([]any{spaceID}, bounds...), cutoff.UTC(), cutoff.UTC()) })
}

func (r *TaskInstanceRepository) cleanupRetentionWindow(
	ctx context.Context,
	spaceID string,
	cutoff time.Time,
	after RetentionCursor,
	window int,
	table, windowEndSQL, candidateSQL, alias string,
	candidateArgs func(bounds []any) []any,
) (deleted int64, next RetentionCursor, err error) {
	if r == nil || r.db == nil {
		return 0, RetentionCursor{}, fmt.Errorf("task instance repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, RetentionCursor{}, fmt.Errorf("space_id is required for scheduled execution detail cleanup")
	}
	if cutoff.IsZero() {
		return 0, RetentionCursor{}, fmt.Errorf("scheduled execution detail cleanup cutoff is required")
	}
	if window <= 0 {
		return 0, after, nil
	}
	window = min(window, MaxRetentionWindowRows)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var end RetentionCursor
		row := tx.Raw(windowEndSQL, spaceID, cutoff.UTC(), after.MTime, after.ID, window-1).Row()
		if scanErr := row.Scan(&end.MTime, &end.ID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return scanErr
		}
		bounds := []any{after.MTime, after.ID}
		upper := ""
		if !end.IsZero() {
			// Fewer than `window` rows remain before the cutoff when no end key
			// exists; that window closes the sweep.
			upper = fmt.Sprintf("AND (%[1]s.c_mtime, %[1]s.c_id) <= (?, ?)", alias)
			bounds = append(bounds, end.MTime, end.ID)
		}
		result := tx.Exec(`DELETE FROM `+table+` WHERE c_id IN (`+fmt.Sprintf(candidateSQL, upper)+`)`, candidateArgs(bounds)...)
		if result.Error != nil {
			return result.Error
		}
		deleted, next = result.RowsAffected, end
		return nil
	})
	if err != nil {
		return 0, after, err
	}
	return deleted, next, nil
}
