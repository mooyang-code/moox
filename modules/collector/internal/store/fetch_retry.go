package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FetchRetryRepository struct{ db *gorm.DB }

func NewFetchRetryRepository(db *gorm.DB) *FetchRetryRepository { return &FetchRetryRepository{db: db} }

func (r *FetchRetryRepository) DeleteByTaskID(ctx context.Context, spaceID, taskID string) error {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	// Only target-scoped retries are owned by one CollectionTask. Fetch retries
	// belong to the shared instance and survive while another WriteTarget exists.
	return r.db.WithContext(ctx).Where(`c_space_id = ? AND c_write_target_id IN (SELECT c_write_target_id FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_task_id = ?)`, spaceID, spaceID, taskID).Delete(&domain.RetryItem{}).Error
}

func (r *FetchRetryRepository) Upsert(ctx context.Context, item *domain.RetryItem) error {
	if item == nil {
		return gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	if item.CreateTime.IsZero() {
		item.CreateTime = now
	}
	item.ModifyTime = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_retry_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_source_batch_id":      clause.Expr{SQL: "CASE WHEN c_source_batch_id <> '' THEN c_source_batch_id ELSE excluded.c_source_batch_id END"},
			"c_batch_kind":           clause.Expr{SQL: "excluded.c_batch_kind"},
			"c_instance_id":          clause.Expr{SQL: "excluded.c_instance_id"},
			"c_write_target_id":      clause.Expr{SQL: "excluded.c_write_target_id"},
			"c_retry_scope":          clause.Expr{SQL: "excluded.c_retry_scope"},
			"c_attempt":              clause.Expr{SQL: "excluded.c_attempt"},
			"c_status":               clause.Expr{SQL: "excluded.c_status"},
			"c_next_retry_at":        clause.Expr{SQL: "excluded.c_next_retry_at"},
			"c_last_error_type":      clause.Expr{SQL: "excluded.c_last_error_type"},
			"c_last_error_summary":   clause.Expr{SQL: "excluded.c_last_error_summary"},
			"c_failure_targets_json": clause.Expr{SQL: "CASE WHEN c_failure_targets_json <> '' AND c_failure_targets_json <> '[]' THEN c_failure_targets_json ELSE excluded.c_failure_targets_json END"},
			"c_mtime":                clause.Expr{SQL: "excluded.c_mtime"},
		}),
	}).Create(item).Error
}

func (r *FetchRetryRepository) ListDue(ctx context.Context, spaceID string, now time.Time, limit int) ([]domain.RetryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []domain.RetryItem
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status = ? AND c_next_retry_at IS NOT NULL AND c_next_retry_at <= ?", spaceID, "pending", now.UTC()).
		Order("c_next_retry_at ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *FetchRetryRepository) Get(ctx context.Context, spaceID, retryKey string) (*domain.RetryItem, error) {
	var item domain.RetryItem
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *FetchRetryRepository) CountPending(ctx context.Context, spaceID, datasetID, frequency string) (int64, error) {
	var count int64
	query := r.db.WithContext(ctx).Table("t_collector_fetch_retry_items AS retries").
		Where("retries.c_space_id = ? AND retries.c_status = ?", spaceID, "pending")
	if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
		query = query.Where(`(
			EXISTS (
				SELECT 1 FROM t_collector_instance_write_targets targets
				WHERE targets.c_space_id = retries.c_space_id
				  AND targets.c_dataset_id = ?
				  AND targets.c_write_target_id = retries.c_write_target_id
			)
			OR (retries.c_write_target_id = '' AND EXISTS (
				SELECT 1 FROM t_collector_instance_write_targets targets
				WHERE targets.c_space_id = retries.c_space_id
				  AND targets.c_dataset_id = ?
				  AND targets.c_instance_id = retries.c_instance_id
			))
		)`, datasetID, datasetID)
	}
	if frequency != "" {
		query = query.Where("retries.c_frequency = ?", frequency)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *FetchRetryRepository) MarkStatus(ctx context.Context, spaceID, retryKey, status string) error {
	return r.db.WithContext(ctx).Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).
		Updates(map[string]any{"c_status": status, "c_mtime": time.Now().UTC()}).Error
}

func (r *FetchRetryRepository) MarkPermanent(ctx context.Context, spaceID, retryKey, errorType, errorSummary string) error {
	spaceID, retryKey = strings.TrimSpace(spaceID), strings.TrimSpace(retryKey)
	if spaceID == "" || retryKey == "" {
		return gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	errorType, errorSummary = strings.TrimSpace(errorType), strings.TrimSpace(errorSummary)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item domain.RetryItem
		if err := tx.Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).First(&item).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if item.Status != "pending" && item.Status != "dispatched" {
			return nil
		}
		result := tx.Model(&domain.RetryItem{}).
			Where("c_space_id = ? AND c_retry_key = ? AND c_status IN ?", spaceID, retryKey, []string{"pending", "dispatched"}).
			Updates(map[string]any{"c_status": "permanent_failed", "c_next_retry_at": nil, "c_last_error_type": errorType, "c_last_error_summary": errorSummary, "c_mtime": now})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if item.InstanceID == "" {
			return nil
		}
		payload, err := json.Marshal(map[string]string{"outcome": "failure", "error_type": errorType, "error_summary": errorSummary})
		if err != nil {
			return err
		}
		if err := tx.Model(&domain.TaskInstance{}).
			Where("c_space_id = ? AND c_instance_id = ?", spaceID, item.InstanceID).
			Updates(map[string]any{"c_last_exec_status": domain.InstanceStatusFailed, "c_last_exec_time": now, "c_result": string(payload), "c_mtime": now}).Error; err != nil {
			return err
		}
		targetQuery := tx.Model(&domain.WriteTarget{}).Where("c_space_id = ? AND c_instance_id = ?", spaceID, item.InstanceID)
		if item.WriteTargetID != "" {
			targetQuery = targetQuery.Where("c_write_target_id = ?", item.WriteTargetID)
		}
		return targetQuery.Updates(map[string]any{"c_status": "failed", "c_last_error": errorSummary, "c_attempt": item.Attempt, "c_mtime": now}).Error
	})
}

func (r *FetchRetryRepository) ListUnreportedPeriodFailures(ctx context.Context, spaceID string, limit int) ([]domain.RetryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []domain.RetryItem
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status = ? AND c_period_failure_reported = ? AND c_failure_targets_json <> '' AND c_failure_targets_json <> '[]'", strings.TrimSpace(spaceID), "permanent_failed", false).
		Order("c_mtime ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *FetchRetryRepository) ListUnreportedPeriodFailuresAfter(ctx context.Context, spaceID, afterRetryKey string, limit int) ([]domain.RetryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []domain.RetryItem
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status = ? AND c_period_failure_reported = ? AND c_failure_targets_json <> '' AND c_failure_targets_json <> '[]' AND c_retry_key > ?", strings.TrimSpace(spaceID), "permanent_failed", false, strings.TrimSpace(afterRetryKey)).
		Order("c_retry_key ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *FetchRetryRepository) MarkPeriodFailureReported(ctx context.Context, spaceID, retryKey string) error {
	return r.db.WithContext(ctx).Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ? AND c_status = ?", strings.TrimSpace(spaceID), strings.TrimSpace(retryKey), "permanent_failed").
		Updates(map[string]any{"c_period_failure_reported": true, "c_mtime": time.Now().UTC()}).Error
}

func (r *FetchRetryRepository) Cleanup(ctx context.Context, before time.Time) error {
	return r.db.WithContext(ctx).
		Where("c_mtime < ? AND (c_status IN ? OR (c_status = ? AND c_period_failure_reported = ?))", before.UTC(), []string{"succeeded", "superseded"}, "permanent_failed", true).
		Delete(&domain.RetryItem{}).Error
}
