package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	maxScheduledWriteTargetCleanupRows = 12000
	maxScheduledInstanceCleanupRows    = 10000
)

const scheduledWriteTargetCleanupPageSpaceSQL = `SELECT targets.c_id
	FROM t_collector_instance_write_targets AS targets INDEXED BY idx_collector_write_targets_retention_space
	JOIN t_collector_task_instances AS instances
	  ON instances.c_space_id = targets.c_space_id AND instances.c_instance_id = targets.c_instance_id
	JOIN t_collector_runs AS runs
	  ON runs.c_space_id = instances.c_space_id AND runs.c_run_id = instances.c_run_id
	WHERE targets.c_space_id = ?
	  AND targets.c_mtime < ?
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
	    JOIN t_collector_task_instances AS newer_instances
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
	    WHERE batches.c_space_id = targets.c_space_id
	      AND (batches.c_write_target_id = targets.c_write_target_id OR batches.c_instance_id = instances.c_instance_id)
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
	ORDER BY targets.c_mtime, targets.c_id
	LIMIT ?`

const scheduledInstanceCleanupPageSpaceSQL = `SELECT instances.c_id
	FROM t_collector_task_instances AS instances INDEXED BY idx_collector_instances_terminal_cleanup_space
	JOIN t_collector_runs AS runs
	  ON runs.c_space_id = instances.c_space_id AND runs.c_run_id = instances.c_run_id
	WHERE instances.c_space_id = ?
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
	ORDER BY instances.c_mtime, instances.c_id
	LIMIT ?`

// CleanupScheduledExecutionDetailsSpace expires old terminal per-run details
// after newer enabled task targets exist. It leaves the latest result for each
// enabled task/Dataset/subject/frequency and protects all resumable work.
func (r *TaskInstanceRepository) CleanupScheduledExecutionDetailsSpace(
	ctx context.Context,
	spaceID string,
	cutoff time.Time,
	targetLimit, instanceLimit int,
) (targetsDeleted, instancesDeleted int64, err error) {
	if r == nil || r.db == nil {
		return 0, 0, fmt.Errorf("task instance repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, 0, fmt.Errorf("space_id is required for scheduled execution detail cleanup")
	}
	if cutoff.IsZero() {
		return 0, 0, fmt.Errorf("scheduled execution detail cleanup cutoff is required")
	}
	if targetLimit <= 0 && instanceLimit <= 0 {
		return 0, 0, nil
	}
	targetLimit = min(targetLimit, maxScheduledWriteTargetCleanupRows)
	instanceLimit = min(instanceLimit, maxScheduledInstanceCleanupRows)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if targetLimit > 0 {
			result := tx.Exec(`DELETE FROM t_collector_instance_write_targets WHERE c_id IN (`+scheduledWriteTargetCleanupPageSpaceSQL+`)`,
				spaceID, cutoff.UTC(), cutoff.UTC(), cutoff.UTC(), targetLimit)
			if result.Error != nil {
				return result.Error
			}
			targetsDeleted = result.RowsAffected
		}
		if instanceLimit > 0 {
			result := tx.Exec(`DELETE FROM t_collector_task_instances WHERE c_id IN (`+scheduledInstanceCleanupPageSpaceSQL+`)`,
				spaceID, cutoff.UTC(), cutoff.UTC(), instanceLimit)
			if result.Error != nil {
				return result.Error
			}
			instancesDeleted = result.RowsAffected
		}
		return nil
	})
	return targetsDeleted, instancesDeleted, err
}
