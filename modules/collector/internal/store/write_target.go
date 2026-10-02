package store

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListEnabledWriteTargetsForInstances returns destinations still owned by enabled
// tasks. It is used immediately before planning/dispatch so task disable races do
// not write into a destination that is no longer active.
func (r *TaskInstanceRepository) ListEnabledWriteTargetsForInstances(ctx context.Context, spaceID string, instanceIDs []string) ([]domain.WriteTarget, error) {
	spaceID = strings.TrimSpace(spaceID)
	instanceIDs = uniqueNonEmptyStrings(instanceIDs)
	if spaceID == "" || len(instanceIDs) == 0 {
		return nil, nil
	}
	var targets []domain.WriteTarget
	err := r.db.WithContext(ctx).Table("t_collector_instance_write_targets AS targets").
		Select("targets.*").
		Joins("JOIN t_collector_tasks AS tasks ON tasks.c_space_id = targets.c_space_id AND tasks.c_task_id = targets.c_task_id").
		Where("targets.c_space_id = ? AND targets.c_instance_id IN ? AND tasks.c_enabled = 1", spaceID, instanceIDs).
		Order("targets.c_instance_id ASC, targets.c_task_id ASC").
		Find(&targets).Error
	return targets, err
}

// UpdateWriteTargetStatus changes only one destination, preserving the fetch
// status and the state of sibling destinations on the shared instance.
func (r *TaskInstanceRepository) UpdateWriteTargetStatus(ctx context.Context, spaceID, targetID, status, lastError string, attempt int) error {
	return r.db.WithContext(ctx).Model(&domain.WriteTarget{}).
		Where("c_space_id = ? AND c_write_target_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(targetID)).
		Updates(map[string]any{"c_status": strings.TrimSpace(status), "c_last_error": lastError, "c_attempt": attempt, "c_mtime": time.Now().UTC()}).Error
}

// GetWriteTarget returns one destination by its stable target identity.
func (r *TaskInstanceRepository) GetWriteTarget(ctx context.Context, spaceID, targetID string) (domain.WriteTarget, error) {
	var target domain.WriteTarget
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_write_target_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(targetID)).First(&target).Error
	return target, err
}

// ListEnabledWriteTargets returns destinations whose owning CollectionTask is
// still enabled. Fetch retries use this relation instead of a single parent
// task so a shared instance remains retryable while any consumer still needs it.
func (r *TaskInstanceRepository) ListEnabledWriteTargets(ctx context.Context, spaceID, instanceID string) ([]domain.WriteTarget, error) {
	var targets []domain.WriteTarget
	err := r.db.WithContext(ctx).Table("t_collector_instance_write_targets AS targets").
		Select("targets.*").
		Joins("JOIN t_collector_tasks AS tasks ON tasks.c_space_id = targets.c_space_id AND tasks.c_task_id = targets.c_task_id").
		Where("targets.c_space_id = ? AND targets.c_instance_id = ? AND tasks.c_enabled = ?", strings.TrimSpace(spaceID), strings.TrimSpace(instanceID), true).
		Order("targets.c_id ASC").Scan(&targets).Error
	return targets, err
}

// ListWriteTargets returns all destinations for one shared instance.
func (r *TaskInstanceRepository) ListWriteTargets(ctx context.Context, spaceID, instanceID string) ([]domain.WriteTarget, error) {
	var targets []domain.WriteTarget
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_instance_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(instanceID)).Order("c_id ASC").Find(&targets).Error
	return targets, err
}

// ListWriteTargetsForInstances returns all destinations for a page of shared
// instances in bounded queries instead of one query per instance.
func (r *TaskInstanceRepository) ListWriteTargetsForInstances(ctx context.Context, spaceID string, instanceIDs []string) (map[string][]domain.WriteTarget, error) {
	spaceID = strings.TrimSpace(spaceID)
	instanceIDs = uniqueNonEmptyStrings(instanceIDs)
	byInstance := make(map[string][]domain.WriteTarget, len(instanceIDs))
	if spaceID == "" || len(instanceIDs) == 0 {
		return byInstance, nil
	}
	for _, instanceID := range instanceIDs {
		byInstance[instanceID] = nil
	}
	for start := 0; start < len(instanceIDs); start += taskInstanceLookupBatchSize {
		end := start + taskInstanceLookupBatchSize
		if end > len(instanceIDs) {
			end = len(instanceIDs)
		}
		var targets []domain.WriteTarget
		if err := r.db.WithContext(ctx).
			Where("c_space_id = ? AND c_instance_id IN ?", spaceID, instanceIDs[start:end]).
			Order("c_instance_id ASC, c_id ASC").Find(&targets).Error; err != nil {
			return nil, err
		}
		for _, target := range targets {
			byInstance[target.InstanceID] = append(byInstance[target.InstanceID], target)
		}
	}
	return byInstance, nil
}

// UpsertWriteTargets atomically attaches task destinations to a shared instance.
func (r *TaskInstanceRepository) UpsertWriteTargets(ctx context.Context, targets []domain.WriteTarget) error {
	if len(targets) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertWriteTargetsTx(tx, targets)
	})
}

func upsertWriteTargetsTx(tx *gorm.DB, targets []domain.WriteTarget) error {
	for _, target := range targets {
		if strings.TrimSpace(target.ID) == "" || strings.TrimSpace(target.SpaceID) == "" || strings.TrimSpace(target.InstanceID) == "" || strings.TrimSpace(target.TaskID) == "" || strings.TrimSpace(target.DatasetID) == "" {
			return gorm.ErrInvalidData
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_instance_id"}, {Name: "c_task_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"c_dataset_id", "c_view_id", "c_output_fields_json", "c_series_index", "c_series_hash", "c_expected_count", "c_mtime"}),
		}).Create(&target).Error; err != nil {
			return err
		}
	}
	return nil
}

// DeleteWriteTargetsByTask detaches one task without deleting shared instances.
func (r *TaskInstanceRepository) DeleteWriteTargetsByTask(ctx context.Context, spaceID, taskID string) error {
	return r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).Delete(&domain.WriteTarget{}).Error
}

// PruneDisabledWriteTargets detaches destinations whose CollectionTask is
// disabled once no in-flight batch can still report that target. Shared
// instances and enabled sibling targets are preserved.
func (r *TaskInstanceRepository) PruneDisabledWriteTargets(ctx context.Context, spaceID string) (int64, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, nil
	}
	// The expensive safety query joins every WriteTarget against batch state.
	// Most scheduler ticks have no disabled CollectionTask at all, so avoid
	// scanning the growing runtime tables unless there is actually something
	// that could be pruned.
	var disabledTasks int64
	if err := r.db.WithContext(ctx).Model(&domain.CollectionTask{}).
		Where("c_space_id = ? AND c_enabled = ?", spaceID, false).Count(&disabledTasks).Error; err != nil {
		return 0, err
	}
	if disabledTasks == 0 {
		return 0, nil
	}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		safeTargets := `SELECT targets.c_write_target_id
			FROM t_collector_instance_write_targets targets
			JOIN t_collector_tasks tasks ON tasks.c_space_id = targets.c_space_id AND tasks.c_task_id = targets.c_task_id
			WHERE targets.c_space_id = ? AND tasks.c_enabled = 0
			AND NOT EXISTS (
				SELECT 1 FROM t_collector_fetch_batch_items items
				JOIN t_collector_fetch_batches batches ON batches.c_space_id = items.c_space_id AND batches.c_batch_id = items.c_batch_id
				WHERE items.c_space_id = targets.c_space_id AND items.c_instance_id = targets.c_instance_id
				AND batches.c_status IN ('planned','dispatched')
			)`
		if err := tx.Exec(`DELETE FROM t_collector_fetch_retry_items WHERE c_space_id = ? AND c_write_target_id IN (`+safeTargets+`) AND NOT (c_status = 'permanent_failed' AND c_period_failure_report_state = 'pending')`, spaceID, spaceID).Error; err != nil {
			return err
		}
		result := tx.Exec(`DELETE FROM t_collector_instance_write_targets WHERE c_write_target_id IN (`+safeTargets+`)`, spaceID)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	return deleted, err
}
