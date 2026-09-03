package store

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FetchBatchRepository struct{ db *gorm.DB }

// MarketFetchInstanceUpdate is the small, stable freshness update emitted by
// the short-lived collector. Keeping it here lets batch completion commit the
// batch, retry state, and task freshness in one SQLite transaction.
type MarketFetchInstanceUpdate struct {
	SpaceID        string
	TaskID         string
	DatasetID      string
	SubjectID      string
	Frequency      string
	TargetDataTime time.Time
	At             time.Time
	Status         int
	Result         string
}

// MarketFetchRetrySupersede marks older pending retry work unnecessary after a
// newer realtime bar for the same governed route was stored successfully.
type MarketFetchRetrySupersede struct {
	SpaceID        string
	DatasetID      string
	SubjectID      string
	Frequency      string
	TargetDataTime time.Time
}

type FetchCompletionEffects struct {
	Retries            []*domain.RetryItem
	SucceededRetryKeys []string
	// CancelPendingRetryKeys resolves a late success only when the retry is
	// still pending. A retry already dispatched to a newer batch must remain
	// dispatched so that its own completion can win the race.
	CancelPendingRetryKeys []string
	PermanentRetryKeys     []string
	// SupersedePendingRetries retires older retry work after a newer realtime
	// success. It also covers already-dispatched retries: their completion is
	// still recorded, but must not regress the current task state.
	SupersedePendingRetries []MarketFetchRetrySupersede
	InstanceUpdates         []MarketFetchInstanceUpdate
}

func NewFetchBatchRepository(db *gorm.DB) *FetchBatchRepository { return &FetchBatchRepository{db: db} }

func (r *FetchBatchRepository) CreatePlanned(ctx context.Context, batch *domain.BatchInvocation) (bool, error) {
	if batch == nil {
		return false, gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	if batch.PlannedAt == nil {
		batch.PlannedAt = &now
	}
	if batch.CreateTime.IsZero() {
		batch.CreateTime = now
	}
	batch.ModifyTime = now
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(batch)
	return result.RowsAffected == 1, result.Error
}

func (r *FetchBatchRepository) Get(ctx context.Context, spaceID, batchID string) (*domain.BatchInvocation, error) {
	var batch domain.BatchInvocation
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_batch_id = ?", spaceID, batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *FetchBatchRepository) MarkDispatched(ctx context.Context, spaceID, batchID, requestID string, deadline time.Time) (bool, error) {
	return r.MarkDispatchedToNode(ctx, spaceID, batchID, requestID, deadline, "", "", "")
}

// MarkDispatchedToNode records the node that accepted the invocation. The
// routing fields are updated in the same planned->dispatched CAS so a
// failover completion is matched against the node that actually ran it.
func (r *FetchBatchRepository) MarkDispatchedToNode(ctx context.Context, spaceID, batchID, requestID string, deadline time.Time, region, nodeID, functionName string) (bool, error) {
	now := time.Now().UTC()
	updates := map[string]any{"c_status": domain.BatchStatusDispatched, "c_request_id": requestID, "c_dispatched_at": now, "c_deadline_at": deadline.UTC(), "c_mtime": now}
	if strings.TrimSpace(region) != "" {
		updates["c_region"] = region
	}
	if strings.TrimSpace(nodeID) != "" {
		updates["c_node_id"] = nodeID
	}
	if strings.TrimSpace(functionName) != "" {
		updates["c_function_name"] = functionName
	}
	result := r.db.WithContext(ctx).Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", spaceID, batchID, domain.BatchStatusPlanned).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func (r *FetchBatchRepository) Complete(ctx context.Context, batch *domain.BatchInvocation) (bool, error) {
	if batch == nil {
		return false, gorm.ErrInvalidData
	}
	updates := map[string]any{
		"c_status": batch.Status, "c_success_count": batch.SuccessCount, "c_retry_count": batch.RetryCount,
		"c_permanent_failed_count": batch.PermanentFailedCount, "c_error_summary": batch.ErrorSummary,
		"c_completed_at": batch.CompletedAt, "c_late_completion": batch.LateCompletion, "c_mtime": time.Now().UTC(),
	}
	query := r.db.WithContext(ctx).Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_batch_id = ?", batch.SpaceID, batch.BatchID)
	if batch.LateCompletion && batch.Status == domain.BatchStatusTimedOut {
		// A timed-out batch accepts only its first late completion. Once the
		// late flag is set, JetStream redelivery must be a no-op rather than
		// consuming another retry attempt.
		query = query.Where("c_status = ? AND c_late_completion = 0", domain.BatchStatusTimedOut)
	} else {
		query = query.Where("c_status NOT IN ?", []domain.BatchStatus{domain.BatchStatusSucceeded, domain.BatchStatusPartialFailed, domain.BatchStatusFailed, domain.BatchStatusTimedOut})
	}
	result := query.Updates(updates)
	return result.RowsAffected == 1, result.Error
}

// CompleteWithEffects atomically records the completion and its retry/freshness
// effects. A duplicate or late terminal completion is a no-op, so EventBus
// redelivery cannot create another retry or regress freshness.
func (r *FetchBatchRepository) CompleteWithEffects(ctx context.Context, batch *domain.BatchInvocation, effects FetchCompletionEffects) (bool, error) {
	if batch == nil {
		return false, gorm.ErrInvalidData
	}
	updated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"c_status": batch.Status, "c_success_count": batch.SuccessCount, "c_retry_count": batch.RetryCount,
			"c_permanent_failed_count": batch.PermanentFailedCount, "c_error_summary": batch.ErrorSummary,
			"c_completed_at": batch.CompletedAt, "c_late_completion": batch.LateCompletion, "c_mtime": time.Now().UTC(),
		}
		query := tx.Model(&domain.BatchInvocation{}).Where("c_space_id = ? AND c_batch_id = ?", batch.SpaceID, batch.BatchID)
		if batch.LateCompletion && batch.Status == domain.BatchStatusTimedOut {
			query = query.Where("c_status = ? AND c_late_completion = 0", domain.BatchStatusTimedOut)
		} else {
			query = query.Where("c_status NOT IN ?", []domain.BatchStatus{domain.BatchStatusSucceeded, domain.BatchStatusPartialFailed, domain.BatchStatusFailed, domain.BatchStatusTimedOut})
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		updated = true
		for _, item := range effects.Retries {
			if item == nil {
				continue
			}
			if item.CreateTime.IsZero() {
				item.CreateTime = time.Now().UTC()
			}
			item.ModifyTime = time.Now().UTC()
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_retry_key"}},
				DoUpdates: clause.Assignments(map[string]any{
					"c_source_batch_id": clause.Expr{SQL: "CASE WHEN c_source_batch_id <> '' THEN c_source_batch_id ELSE excluded.c_source_batch_id END"}, "c_batch_kind": clause.Expr{SQL: "excluded.c_batch_kind"}, "c_attempt": clause.Expr{SQL: "excluded.c_attempt"},
					"c_status": clause.Expr{SQL: "CASE WHEN c_status IN ('succeeded', 'permanent_failed') THEN c_status ELSE excluded.c_status END"}, "c_next_retry_at": clause.Expr{SQL: "excluded.c_next_retry_at"},
					"c_last_error_type": clause.Expr{SQL: "excluded.c_last_error_type"}, "c_last_error_summary": clause.Expr{SQL: "excluded.c_last_error_summary"},
					"c_mtime": clause.Expr{SQL: "excluded.c_mtime"},
				}),
			}).Create(item).Error; err != nil {
				return err
			}
		}
		for _, key := range effects.SucceededRetryKeys {
			if err := tx.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ?", batch.SpaceID, key).Updates(map[string]any{"c_status": "succeeded", "c_mtime": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		for _, key := range effects.CancelPendingRetryKeys {
			if err := tx.Model(&domain.RetryItem{}).
				Where("c_space_id = ? AND c_retry_key = ? AND c_status = ?", batch.SpaceID, key, "pending").
				Updates(map[string]any{"c_status": "succeeded", "c_mtime": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		for _, key := range effects.PermanentRetryKeys {
			if err := tx.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ?", batch.SpaceID, key).Updates(map[string]any{"c_status": "permanent_failed", "c_mtime": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		// A realtime batch contains dozens of symbols. Updating retry state one
		// symbol at a time serializes the SQLite writer and can delay completion
		// event handling long enough to make healthy SCF calls look timed out.
		// Collapse the batch's supersede operations into one UPDATE instead.
		conditions := make([]string, 0, len(effects.SupersedePendingRetries))
		args := make([]any, 0, len(effects.SupersedePendingRetries)*5)
		seen := make(map[string]struct{}, len(effects.SupersedePendingRetries))
		for _, item := range effects.SupersedePendingRetries {
			if item.SpaceID == "" || item.DatasetID == "" || item.SubjectID == "" || item.Frequency == "" || item.TargetDataTime.IsZero() {
				continue
			}
			key := strings.Join([]string{item.SpaceID, item.DatasetID, item.SubjectID, item.Frequency, item.TargetDataTime.UTC().Format(time.RFC3339Nano)}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			conditions = append(conditions, "(c_space_id = ? AND c_dataset_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_target_data_time <= ?)")
			args = append(args, item.SpaceID, item.DatasetID, item.SubjectID, item.Frequency, item.TargetDataTime.UTC())
		}
		if len(conditions) > 0 {
			query := tx.Model(&domain.RetryItem{}).
				Where("c_status IN ?", []string{"pending", "dispatched"}).
				Where(strings.Join(conditions, " OR "), args...)
			if err := query.Updates(map[string]any{"c_status": "superseded", "c_mtime": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		for _, item := range effects.InstanceUpdates {
			query := tx.Model(&domain.TaskInstance{}).Where("c_space_id = ? AND c_dataset_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_is_deleted = ?", item.SpaceID, item.DatasetID, item.SubjectID, item.Frequency, false)
			if item.TaskID != "" {
				query = query.Where("c_task_id = ?", item.TaskID)
			}
			if !item.TargetDataTime.IsZero() {
				// Completion order is not data order: an older SCF invocation can
				// finish after the next period has already succeeded. Keep the
				// instance state for the newest covered market-data timestamp.
				query = query.Where("CASE WHEN json_valid(c_result) THEN COALESCE(CAST(json_extract(c_result, '$.target_data_unix') AS INTEGER), -1) ELSE -1 END <= ?", item.TargetDataTime.UTC().Unix())
			}
			if err := query.Updates(map[string]any{
				"c_last_exec_status": item.Status, "c_last_exec_time": item.At.UTC(), "c_result": normalizeJSON(item.Result), "c_mtime": time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return updated, err
}

func (r *FetchBatchRepository) ListDue(ctx context.Context, spaceID string, now time.Time, limit int) ([]domain.BatchInvocation, error) {
	if limit <= 0 {
		limit = 100
	}
	var batches []domain.BatchInvocation
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status IN ? AND c_deadline_at IS NOT NULL AND c_deadline_at <= ?", spaceID, []domain.BatchStatus{domain.BatchStatusPlanned, domain.BatchStatusDispatched}, now.UTC()).
		Order("c_deadline_at ASC").Limit(limit).Find(&batches).Error
	return batches, err
}

// HasActiveTask prevents non-realtime recovery work from competing with a
// realtime batch for the same logical task. Task IDs are stable and appear in
// the persisted request JSON, while the batch table intentionally stays free
// of per-item columns.
func (r *FetchBatchRepository) HasActiveTask(ctx context.Context, spaceID, taskID string, kinds ...domain.BatchKind) (bool, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false, nil
	}
	query := r.db.WithContext(ctx).Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_status IN ? AND c_request_json LIKE ?", spaceID, []domain.BatchStatus{domain.BatchStatusPlanned, domain.BatchStatusDispatched}, `%"task_id":"`+taskID+`"%`)
	if len(kinds) > 0 {
		query = query.Where("c_batch_kind IN ?", kinds)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *FetchBatchRepository) Cleanup(ctx context.Context, successBefore, failureBefore time.Time) error {
	if err := r.db.WithContext(ctx).Where("c_status = ? AND c_completed_at IS NOT NULL AND c_completed_at < ?", domain.BatchStatusSucceeded, successBefore.UTC()).Delete(&domain.BatchInvocation{}).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("c_status IN ? AND c_completed_at IS NOT NULL AND c_completed_at < ?", []domain.BatchStatus{domain.BatchStatusPartialFailed, domain.BatchStatusFailed, domain.BatchStatusTimedOut}, failureBefore.UTC()).Delete(&domain.BatchInvocation{}).Error
}
