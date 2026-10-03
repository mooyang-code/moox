package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const maxScheduledRunCleanupRows = 4000

const scheduledRunCleanupPageSQL = `SELECT runs.c_id
	FROM t_collector_runs AS runs INDEXED BY idx_collector_runs_terminal_cleanup
	WHERE runs.c_run_type = 'scheduled'
	  AND runs.c_status IN ('succeeded','partial_failed','failed')
	  AND runs.c_mtime < ?
	  AND EXISTS (
		SELECT 1 FROM t_collector_runs AS newer
		WHERE newer.c_space_id = runs.c_space_id AND newer.c_run_type = 'scheduled' AND newer.c_id > runs.c_id
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM t_collector_task_instances AS instances
		WHERE instances.c_space_id = runs.c_space_id AND instances.c_run_id = runs.c_run_id
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM t_collector_timer_period_batches AS manifests
		WHERE manifests.c_space_id = runs.c_space_id AND manifests.c_first_run_id = runs.c_run_id
	  )
	ORDER BY runs.c_mtime, runs.c_id
	LIMIT ?`

const scheduledRunCleanupPageSpaceSQL = `SELECT runs.c_id
	FROM t_collector_runs AS runs INDEXED BY idx_collector_runs_terminal_cleanup_space
	WHERE runs.c_space_id = ?
	  AND runs.c_run_type = 'scheduled'
	  AND runs.c_status IN ('succeeded','partial_failed','failed')
	  AND runs.c_mtime < ?
	  AND EXISTS (
		SELECT 1 FROM t_collector_runs AS newer
		WHERE newer.c_space_id = runs.c_space_id AND newer.c_run_type = 'scheduled' AND newer.c_id > runs.c_id
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM t_collector_task_instances AS instances
		WHERE instances.c_space_id = runs.c_space_id AND instances.c_run_id = runs.c_run_id
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM t_collector_timer_period_batches AS manifests
		WHERE manifests.c_space_id = runs.c_space_id AND manifests.c_first_run_id = runs.c_run_id
	  )
	ORDER BY runs.c_mtime, runs.c_id
	LIMIT ?`

// CleanupScheduledTerminalBefore removes only old scheduled summaries that no
// longer own retained TaskInstances, Timer manifests, or the latest scheduler
// position for their Space. The companion execution-detail cleanup preserves
// only the latest result for each enabled task target.
func (r *RunRepository) CleanupScheduledTerminalBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	return r.cleanupScheduledTerminalBefore(ctx, "", cutoff, limit)
}

// CleanupScheduledTerminalSpace keeps per-Space Run retention independent so
// high-volume Spaces cannot consume another Space's cleanup budget.
func (r *RunRepository) CleanupScheduledTerminalSpace(ctx context.Context, spaceID string, cutoff time.Time, limit int) (int64, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required for scoped scheduled Run cleanup")
	}
	return r.cleanupScheduledTerminalBefore(ctx, spaceID, cutoff, limit)
}

func (r *RunRepository) cleanupScheduledTerminalBefore(ctx context.Context, spaceID string, cutoff time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("run repository is not initialized")
	}
	if cutoff.IsZero() {
		return 0, fmt.Errorf("scheduled run cleanup cutoff is required")
	}
	if limit <= 0 {
		return 0, nil
	}
	limit = min(limit, maxScheduledRunCleanupRows)
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := scheduledRunCleanupPageSQL
		args := []any{cutoff.UTC(), limit}
		if spaceID != "" {
			query = scheduledRunCleanupPageSpaceSQL
			args = []any{spaceID, cutoff.UTC(), limit}
		}
		result := tx.Exec(`DELETE FROM t_collector_runs WHERE c_id IN (`+query+`)`, args...)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	return deleted, err
}
