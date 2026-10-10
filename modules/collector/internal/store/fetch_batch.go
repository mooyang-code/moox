package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidCompletionScope = errors.New("completion effects violate batch scope")

// Terminal batch cleanup includes timed-out batches that never received a
// late completion: c_completed_at < terminalBefore (the execution-detail
// retention) bounds how long a late receipt is waited for. Without that bound
// such batches, their items, and every row they reference were kept forever.
// A batch still bound to a Timer period manifest keeps its completion scope;
// the manifest lifecycle decides when that late completion can no longer come.
const (
	maxFetchBatchCleanupItemRows = 12000
	maxFetchBatchCleanupBatches  = 500

	fetchBatchCleanupItemsPageSQL = `SELECT items.rowid
		FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_items_cleanup
		CROSS JOIN t_collector_fetch_batch_items AS items
		WHERE batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ? AND batches.c_items_cleaned = 0
		  AND NOT (batches.c_status = 'timed_out' AND batches.c_late_completion = 0 AND EXISTS (
			SELECT 1 FROM t_collector_timer_period_batches AS manifests
			WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
		  ))
		  AND items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
		ORDER BY batches.c_completed_at, batches.c_id
		LIMIT ?`
	fetchBatchCleanupItemsPageSpaceSQL = `SELECT items.rowid
		FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_items_cleanup_space
		CROSS JOIN t_collector_fetch_batch_items AS items
		WHERE batches.c_space_id = ? AND batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ? AND batches.c_items_cleaned = 0
		  AND NOT (batches.c_status = 'timed_out' AND batches.c_late_completion = 0 AND EXISTS (
			SELECT 1 FROM t_collector_timer_period_batches AS manifests
			WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
		  ))
		  AND items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
		ORDER BY batches.c_completed_at, batches.c_id
		LIMIT ?`
	fetchBatchCleanupBatchesPageSQL = `SELECT batches.c_id
		FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_parent_cleanup
		WHERE batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ?
		  AND batches.c_items_cleaned = 1
		  AND NOT EXISTS (
			SELECT 1 FROM t_collector_fetch_batch_items AS items
			WHERE items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM t_collector_timer_period_batches AS manifests
			WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
		  )
		ORDER BY batches.c_completed_at, batches.c_id
		LIMIT ?`
	fetchBatchCleanupBatchesPageSpaceSQL = `SELECT batches.c_id
		FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_parent_cleanup_space
		WHERE batches.c_space_id = ? AND batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ?
		  AND batches.c_items_cleaned = 1
		  AND NOT EXISTS (
			SELECT 1 FROM t_collector_fetch_batch_items AS items
			WHERE items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM t_collector_timer_period_batches AS manifests
			WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
		  )
		ORDER BY batches.c_completed_at, batches.c_id
		LIMIT ?`
)

type FetchBatchRepository struct {
	db              *gorm.DB
	retryMu         sync.Mutex
	retryStatements [completionRetryBatchSize]*sql.Stmt
}

// MarketFetchInstanceUpdate is the small, stable freshness update emitted by
// the short-lived collector. Keeping it here lets batch completion commit the
// batch, retry state, and task freshness in one SQLite transaction.
type MarketFetchInstanceUpdate struct {
	SpaceID        string
	RetrySourceKey string
	// InstanceID narrows the freshness update to one stable TaskInstance.
	InstanceID     string
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
	RetrySourceKey string
	SubjectID      string
	Frequency      string
	TargetDataTime time.Time
	InstanceID     string
	WriteTargetID  string
	RetryScope     string
}

type WriteTargetStatusEffect struct {
	SpaceID        string
	RetrySourceKey string
	InstanceID     string
	WriteTargetID  string
	DatasetID      string
	Status         string
	LastError      string
	Attempt        int
}

type RetryTerminalError struct {
	ErrorType    string
	ErrorSummary string
}

type FetchCompletionEffects struct {
	// RetrySourceGuards maps each effect retry key to the retry key that
	// produced it. Completion rechecks these source keys in the same transaction
	// so late attempts cannot overwrite a newer terminal retry result.
	RetrySourceGuards  map[string]string
	WriteTargetUpdates []WriteTargetStatusEffect
	Retries            []*domain.RetryItem
	SucceededRetryKeys []string
	// CancelPendingRetryKeys resolves a late success only when the retry is
	// still pending. A retry already dispatched to a newer batch must remain
	// dispatched so that its own completion can win the race.
	CancelPendingRetryKeys []string
	PermanentRetryKeys     []string
	PermanentRetryErrors   map[string]RetryTerminalError
	// SupersedePendingRetries retires older retry work after a newer realtime
	// success. It also covers already-dispatched retries: their completion is
	// still recorded, but must not regress the current task state.
	SupersedePendingRetries []MarketFetchRetrySupersede
	InstanceUpdates         []MarketFetchInstanceUpdate
}

func NewFetchBatchRepository(db *gorm.DB) *FetchBatchRepository { return &FetchBatchRepository{db: db} }

// CreatePlanned persists a batch with no single task/Dataset owner. Production
// scheduling should prefer CreatePlannedWithItemsForEnabledTargets so the batch
// and its shared instance membership are committed under the same race barrier.
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

// CreatePlannedWithItemsForEnabledTargets atomically persists a batch and its
// instance membership only when at least one attached WriteTarget still belongs
// to an enabled CollectionTask. This is the shared-acquisition race barrier: a
// disabled first task must not suppress another enabled target on the same batch.
func (r *FetchBatchRepository) CreatePlannedWithItemsForEnabledTargets(ctx context.Context, batch *domain.BatchInvocation, instanceIDs []string) (bool, error) {
	if batch == nil {
		return false, gorm.ErrInvalidData
	}
	spaceID := strings.TrimSpace(batch.SpaceID)
	batchID := strings.TrimSpace(batch.BatchID)
	instanceIDs = uniqueNonEmptyStrings(instanceIDs)
	if spaceID == "" || batchID == "" || len(instanceIDs) == 0 {
		return false, gorm.ErrInvalidData
	}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var enabledTargets int64
		if err := tx.Table("t_collector_instance_write_targets AS targets").
			Joins("JOIN t_collector_tasks AS tasks ON tasks.c_space_id = targets.c_space_id AND tasks.c_task_id = targets.c_task_id").
			Where("targets.c_space_id = ? AND targets.c_instance_id IN ? AND tasks.c_enabled = 1", spaceID, instanceIDs).
			Count(&enabledTargets).Error; err != nil {
			return err
		}
		if enabledTargets == 0 {
			return nil
		}
		now := time.Now().UTC()
		if batch.PlannedAt == nil {
			batch.PlannedAt = &now
		}
		if batch.CreateTime.IsZero() {
			batch.CreateTime = now
		}
		batch.ModifyTime = now
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(batch)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		for _, instanceID := range instanceIDs {
			if err := tx.Exec(`INSERT INTO t_collector_fetch_batch_items(c_space_id,c_batch_id,c_instance_id) VALUES(?,?,?) ON CONFLICT(c_space_id,c_batch_id,c_instance_id) DO NOTHING`, spaceID, batchID, instanceID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return created, err
}

// UpsertItems persists the shared TaskInstances contained in a batch. The
// relation is deliberately independent of BatchInvocation.TaskID/DatasetID:
// one batch can serve WriteTargets owned by multiple CollectionTasks.
func (r *FetchBatchRepository) UpsertItems(ctx context.Context, spaceID, batchID string, instanceIDs []string) error {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	if spaceID == "" || batchID == "" {
		return gorm.ErrInvalidData
	}
	seen := make(map[string]struct{}, len(instanceIDs))
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, instanceID := range instanceIDs {
			instanceID = strings.TrimSpace(instanceID)
			if instanceID == "" {
				continue
			}
			if _, ok := seen[instanceID]; ok {
				continue
			}
			seen[instanceID] = struct{}{}
			if err := tx.Exec(`INSERT INTO t_collector_fetch_batch_items(c_space_id,c_batch_id,c_instance_id) VALUES(?,?,?) ON CONFLICT(c_space_id,c_batch_id,c_instance_id) DO NOTHING`, spaceID, batchID, instanceID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *FetchBatchRepository) Get(ctx context.Context, spaceID, batchID string) (*domain.BatchInvocation, error) {
	var batch domain.BatchInvocation
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_batch_id = ?", spaceID, batchID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *FetchBatchRepository) GetTimerPeriodManifest(ctx context.Context, spaceID, batchID string) (*domain.TimerPeriodBatch, error) {
	var manifest domain.TimerPeriodBatch
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_batch_id = ?", spaceID, batchID).First(&manifest).Error
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (r *FetchBatchRepository) ListItems(ctx context.Context, spaceID, batchID string) ([]string, error) {
	var items []struct {
		InstanceID string `gorm:"column:c_instance_id"`
	}
	err := r.db.WithContext(ctx).Table("t_collector_fetch_batch_items").
		Select("c_instance_id").Where("c_space_id = ? AND c_batch_id = ?", spaceID, batchID).
		Order("c_instance_id ASC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.InstanceID)
	}
	return result, nil
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

// MarkRetryBatchDispatched atomically records an accepted Invoke and marks the
// retries that remain pending as dispatched. A retry may become terminal after
// preflight while Invoke is in flight; that must not roll back the accepted batch.
func (r *FetchBatchRepository) MarkRetryBatchDispatched(ctx context.Context, spaceID, batchID, requestID string, deadline time.Time, region, nodeID, functionName string, retryKeys []string) (bool, error) {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	if spaceID == "" || batchID == "" || len(retryKeys) == 0 {
		return false, gorm.ErrInvalidData
	}
	keys := make([]string, 0, len(retryKeys))
	seen := make(map[string]struct{}, len(retryKeys))
	for _, retryKey := range retryKeys {
		retryKey = strings.TrimSpace(retryKey)
		if retryKey == "" {
			return false, gorm.ErrInvalidData
		}
		if _, exists := seen[retryKey]; exists {
			return false, gorm.ErrInvalidData
		}
		seen[retryKey] = struct{}{}
		keys = append(keys, retryKey)
	}

	updated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		updates := map[string]any{
			"c_status": domain.BatchStatusDispatched, "c_request_id": requestID,
			"c_dispatched_at": now, "c_deadline_at": deadline.UTC(), "c_mtime": now,
		}
		if strings.TrimSpace(region) != "" {
			updates["c_region"] = region
		}
		if strings.TrimSpace(nodeID) != "" {
			updates["c_node_id"] = nodeID
		}
		if strings.TrimSpace(functionName) != "" {
			updates["c_function_name"] = functionName
		}
		result := tx.Model(&domain.BatchInvocation{}).
			Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", spaceID, batchID, domain.BatchStatusPlanned).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}

		// Probe this batch's retry identities directly. With an entire failed
		// fleet pending, SQLite otherwise chooses a status index and scans the
		// shrinking pending wave once for every dispatched batch.
		result = tx.Table("t_collector_fetch_retry_items INDEXED BY idx_collector_fetch_retry").
			Where("c_space_id = ? AND c_retry_key IN ? AND c_status = ?", spaceID, keys, "pending").
			Updates(map[string]any{"c_status": "dispatched", "c_mtime": now})
		if result.Error != nil {
			return result.Error
		}
		updated = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

// RefreshPlannedRetryDeadline renews the bounded dispatch window when a
// persisted planned retry batch is resumed after a process restart.
func (r *FetchBatchRepository) RefreshPlannedRetryDeadline(ctx context.Context, spaceID, batchID string, deadline time.Time) (bool, error) {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	if spaceID == "" || batchID == "" || deadline.IsZero() {
		return false, gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", spaceID, batchID, domain.BatchStatusPlanned).
		Updates(map[string]any{"c_deadline_at": deadline.UTC(), "c_mtime": now})
	return result.RowsAffected == 1, result.Error
}

// MakePlannedRetryBatchDue lets ordinary recovery own a batch after all Invoke
// candidates have failed. It does not change retry keys or batch status.
func (r *FetchBatchRepository) MakePlannedRetryBatchDue(ctx context.Context, spaceID, batchID string, dueAt time.Time) (bool, error) {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	if spaceID == "" || batchID == "" || dueAt.IsZero() {
		return false, gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&domain.BatchInvocation{}).
		Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", spaceID, batchID, domain.BatchStatusPlanned).
		Updates(map[string]any{"c_deadline_at": dueAt.UTC(), "c_mtime": now})
	return result.RowsAffected == 1, result.Error
}

// PrepareRetryBatchDispatch rejects a persisted request if any of its retry
// keys became terminal while it waited for Invoke capacity. The pending keys
// remain queued so a later scheduler pass can build a fresh compatible batch.
func (r *FetchBatchRepository) PrepareRetryBatchDispatch(ctx context.Context, spaceID, batchID string, retryKeys []string) (bool, error) {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	if spaceID == "" || batchID == "" || len(retryKeys) == 0 {
		return false, gorm.ErrInvalidData
	}
	keys := make([]string, 0, len(retryKeys))
	seen := make(map[string]struct{}, len(retryKeys))
	for _, retryKey := range retryKeys {
		retryKey = strings.TrimSpace(retryKey)
		if retryKey == "" {
			return false, gorm.ErrInvalidData
		}
		if _, exists := seen[retryKey]; exists {
			return false, gorm.ErrInvalidData
		}
		seen[retryKey] = struct{}{}
		keys = append(keys, retryKey)
	}

	eligible := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch domain.BatchInvocation
		if err := tx.Select("c_status").Where("c_space_id = ? AND c_batch_id = ?", spaceID, batchID).First(&batch).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if batch.Status != domain.BatchStatusPlanned {
			return nil
		}

		var states []struct {
			RetryKey string `gorm:"column:c_retry_key"`
			Status   string `gorm:"column:c_status"`
		}
		if err := tx.Model(&domain.RetryItem{}).
			Select("c_retry_key, c_status").
			Where("c_space_id = ? AND c_retry_key IN ?", spaceID, keys).
			Find(&states).Error; err != nil {
			return err
		}
		allPending := len(states) == len(keys)
		for _, state := range states {
			if state.Status != "pending" {
				allPending = false
				break
			}
		}
		if allPending {
			eligible = true
			return nil
		}

		now := time.Now().UTC()
		result := tx.Model(&domain.BatchInvocation{}).
			Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", spaceID, batchID, domain.BatchStatusPlanned).
			Updates(map[string]any{
				"c_status": domain.BatchStatusFailed, "c_completed_at": now,
				"c_error_summary": "retry batch canceled because one or more retry keys are no longer pending",
				"c_mtime":         now,
			})
		return result.Error
	})
	if err != nil {
		return false, err
	}
	return eligible, nil
}

func (r *FetchBatchRepository) Complete(ctx context.Context, batch *domain.BatchInvocation) (bool, error) {
	if batch == nil {
		return false, gorm.ErrInvalidData
	}
	if !batch.Status.Terminal() {
		return false, fmt.Errorf("market fetch batch completion status %q is not terminal", batch.Status)
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
	if !batch.Status.Terminal() {
		return false, fmt.Errorf("market fetch batch completion status %q is not terminal", batch.Status)
	}
	var retryStatements [completionRetryBatchSize]*sql.Stmt
	if len(effects.Retries) > 0 {
		var err error
		retryStatements, err = r.prepareCompletionRetries(ctx)
		if err != nil {
			return false, err
		}
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
		if err := validateCompletionScope(tx, batch, effects); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidCompletionScope, err)
		}
		terminalRetrySources := make(map[string]struct{})
		sourceKeys := make([]string, 0, len(effects.RetrySourceGuards)+len(effects.WriteTargetUpdates)+len(effects.InstanceUpdates))
		for _, sourceKey := range effects.RetrySourceGuards {
			sourceKeys = append(sourceKeys, sourceKey)
		}
		for _, item := range effects.WriteTargetUpdates {
			sourceKeys = append(sourceKeys, item.RetrySourceKey)
		}
		for _, item := range effects.InstanceUpdates {
			sourceKeys = append(sourceKeys, item.RetrySourceKey)
		}
		for _, item := range effects.SupersedePendingRetries {
			sourceKeys = append(sourceKeys, item.RetrySourceKey)
		}
		sourceKeys = uniqueNonEmptyStrings(sourceKeys)
		if len(sourceKeys) > 0 {
			var retryStates []struct {
				RetryKey string `gorm:"column:c_retry_key"`
				Status   string `gorm:"column:c_status"`
			}
			if err := tx.Model(&domain.RetryItem{}).Select("c_retry_key, c_status").
				Where("c_space_id = ? AND c_retry_key IN ?", batch.SpaceID, sourceKeys).Find(&retryStates).Error; err != nil {
				return err
			}
			for _, retry := range retryStates {
				switch retry.Status {
				case "succeeded", "permanent_failed", "superseded":
					terminalRetrySources[retry.RetryKey] = struct{}{}
				}
			}
		}
		sourceIsTerminal := func(sourceKey string) bool {
			_, terminal := terminalRetrySources[strings.TrimSpace(sourceKey)]
			return terminal
		}
		effectSourceKey := func(effectKey string) string {
			effectKey = strings.TrimSpace(effectKey)
			if sourceKey := strings.TrimSpace(effects.RetrySourceGuards[effectKey]); sourceKey != "" {
				return sourceKey
			}
			return effectKey
		}
		for _, item := range effects.WriteTargetUpdates {
			if sourceIsTerminal(item.RetrySourceKey) {
				continue
			}
			result := tx.Model(&domain.WriteTarget{}).
				Where("c_space_id = ? AND c_write_target_id = ? AND c_instance_id = ? AND c_dataset_id = ?", item.SpaceID, item.WriteTargetID, item.InstanceID, item.DatasetID).
				Updates(map[string]any{"c_status": strings.TrimSpace(item.Status), "c_last_error": item.LastError, "c_attempt": item.Attempt, "c_mtime": time.Now().UTC()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("completion write target %s changed during transaction", item.WriteTargetID)
			}
		}
		transactionStatements := make(map[int]*sql.Stmt)
		defer func() {
			for _, statement := range transactionStatements {
				_ = statement.Close()
			}
		}()
		writeRetries := func(arguments []any) error {
			count := len(arguments) / len(completionRetryColumns)
			if count == 0 {
				return nil
			}
			statement := transactionStatements[count]
			if statement == nil {
				transaction, ok := tx.Statement.ConnPool.(*sql.Tx)
				if !ok {
					return gorm.ErrInvalidTransaction
				}
				statement = transaction.StmtContext(ctx, retryStatements[count-1])
				transactionStatements[count] = statement
			}
			_, err := statement.ExecContext(ctx, arguments...)
			return err
		}
		arguments := make([]any, 0, completionRetryBatchSize*len(completionRetryColumns))
		retryTime := time.Now().UTC()
		for _, item := range effects.Retries {
			if item == nil {
				continue
			}
			if sourceIsTerminal(effectSourceKey(item.RetryKey)) {
				continue
			}
			if item.PeriodFailureReportState == "" {
				item.PeriodFailureReportState = domain.PeriodFailureReportPending
			}
			if item.PeriodFailureResultsJSON == "" {
				item.PeriodFailureResultsJSON = "[]"
			}
			if item.CreateTime.IsZero() {
				item.CreateTime = retryTime
			}
			item.ModifyTime = retryTime
			// Retry keys identify completion effects. IDs are never consumed by
			// the caller; omit RETURNING to avoid a temporary SQLite table for every row.
			retry := map[string]any{
				"c_space_id": item.SpaceID, "c_retry_key": item.RetryKey, "c_source_batch_id": item.SourceBatchID,
				"c_batch_kind": item.BatchKind, "c_instance_id": item.InstanceID, "c_write_target_id": item.WriteTargetID,
				"c_retry_scope": item.RetryScope, "c_subject_id": item.SubjectID, "c_frequency": item.Frequency,
				"c_target_data_time": item.TargetDataTime, "c_period_time": item.PeriodTime, "c_period_deadline_at": item.PeriodDeadlineAt,
				"c_task_json": item.TaskJSON, "c_failure_targets_json": item.FailureTargetsJSON, "c_attempt": item.Attempt,
				"c_status": item.Status, "c_period_failure_report_state": item.PeriodFailureReportState,
				"c_period_failure_results_json": item.PeriodFailureResultsJSON, "c_period_failure_last_error": item.PeriodFailureLastError,
				"c_period_failure_deadline_exceeded_at": item.PeriodFailureDeadlineExceededAt, "c_next_retry_at": item.NextRetryAt,
				"c_last_error_type": item.LastErrorType, "c_last_error_summary": item.LastErrorSummary,
				"c_ctime": item.CreateTime, "c_mtime": item.ModifyTime,
			}
			for _, column := range completionRetryColumns {
				arguments = append(arguments, retry[column])
			}
			if len(arguments) == cap(arguments) {
				if err := writeRetries(arguments); err != nil {
					return err
				}
				arguments = arguments[:0]
			}
		}
		if err := writeRetries(arguments); err != nil {
			return err
		}

		for _, key := range effects.SucceededRetryKeys {
			if sourceIsTerminal(effectSourceKey(key)) {
				continue
			}
			if err := tx.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ? AND c_status NOT IN ?", batch.SpaceID, key, []string{"permanent_failed", "superseded"}).Updates(map[string]any{
				"c_status": "succeeded", "c_period_failure_report_state": domain.PeriodFailureReportPending,
				"c_period_failure_results_json": "[]", "c_period_failure_last_error": "",
				"c_period_failure_deadline_exceeded_at": nil, "c_mtime": time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
		}
		for _, key := range effects.CancelPendingRetryKeys {
			if sourceIsTerminal(effectSourceKey(key)) {
				continue
			}
			if err := tx.Model(&domain.RetryItem{}).
				Where("c_space_id = ? AND c_retry_key = ? AND c_status = ?", batch.SpaceID, key, "pending").
				Updates(map[string]any{
					"c_status": "succeeded", "c_period_failure_report_state": domain.PeriodFailureReportPending,
					"c_period_failure_results_json": "[]", "c_period_failure_last_error": "",
					"c_period_failure_deadline_exceeded_at": nil, "c_mtime": time.Now().UTC(),
				}).Error; err != nil {
				return err
			}
		}
		for _, key := range effects.PermanentRetryKeys {
			if sourceIsTerminal(effectSourceKey(key)) {
				continue
			}
			updates := map[string]any{"c_status": "permanent_failed", "c_mtime": time.Now().UTC()}
			if terminal, ok := effects.PermanentRetryErrors[key]; ok {
				updates["c_last_error_type"] = terminal.ErrorType
				updates["c_last_error_summary"] = terminal.ErrorSummary
			}
			if err := tx.Model(&domain.RetryItem{}).
				Where("c_space_id = ? AND c_retry_key = ? AND c_status NOT IN ?", batch.SpaceID, key, []string{"succeeded", "permanent_failed", "superseded"}).Updates(updates).Error; err != nil {
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
			if sourceIsTerminal(item.RetrySourceKey) {
				continue
			}
			if item.SpaceID == "" || item.SubjectID == "" || item.Frequency == "" || item.TargetDataTime.IsZero() {
				continue
			}
			// Shared instances are retried per write target. Prefer the new
			// identity when present; the legacy dataset key remains a fallback.
			if item.WriteTargetID != "" {
				if err := tx.Model(&domain.RetryItem{}).
					Where("c_space_id = ? AND c_write_target_id = ? AND c_instance_id = ? AND c_target_data_time <= ? AND c_status IN ?", item.SpaceID, item.WriteTargetID, item.InstanceID, item.TargetDataTime.UTC(), []string{"pending", "dispatched"}).
					Updates(map[string]any{"c_status": "superseded", "c_mtime": time.Now().UTC()}).Error; err != nil {
					return err
				}
				continue
			}
			if item.InstanceID == "" {
				continue
			}
			key := strings.Join([]string{item.SpaceID, item.InstanceID, item.SubjectID, item.Frequency, item.TargetDataTime.UTC().Format(time.RFC3339Nano)}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			conditions = append(conditions, "(c_space_id = ? AND c_instance_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_target_data_time <= ?)")
			args = append(args, item.SpaceID, item.InstanceID, item.SubjectID, item.Frequency, item.TargetDataTime.UTC())
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
			if sourceIsTerminal(item.RetrySourceKey) {
				continue
			}
			query := tx.Model(&domain.TaskInstance{}).Where("t_collector_task_instances.c_space_id = ? AND c_subject_id = ? AND c_frequency = ? AND c_is_deleted = ?", item.SpaceID, item.SubjectID, item.Frequency, false)
			if item.InstanceID != "" {
				query = query.Where("c_instance_id = ?", item.InstanceID)
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
	if err != nil {
		// The Batch CAS and every effect live in the same transaction. A
		// validation/effect error rolls the CAS back as well, so callers must
		// observe updated=false whenever nothing committed.
		return false, err
	}
	return updated, nil
}

func validateCompletionScope(tx *gorm.DB, batch *domain.BatchInvocation, effects FetchCompletionEffects) error {
	if tx == nil || batch == nil {
		return gorm.ErrInvalidData
	}
	// Scope membership is immutable within this writer transaction. Fetch it
	// once instead of performing a COUNT for every item in a timeout wave.
	members := make(map[string]bool)
	if len(effects.InstanceUpdates)+len(effects.Retries)+len(effects.WriteTargetUpdates) > 0 {
		var ids []string
		if err := tx.Table("t_collector_fetch_batch_items").Where("c_space_id = ? AND c_batch_id = ?", batch.SpaceID, batch.BatchID).Pluck("c_instance_id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			members[id] = true
		}
	}
	validateInstance := func(spaceID, instanceID string) error {
		spaceID, instanceID = strings.TrimSpace(spaceID), strings.TrimSpace(instanceID)
		if spaceID == "" {
			spaceID = strings.TrimSpace(batch.SpaceID)
		}
		if instanceID == "" {
			return gorm.ErrInvalidData
		}
		if spaceID != strings.TrimSpace(batch.SpaceID) || !members[instanceID] {
			return fmt.Errorf("completion instance %s is not attached to batch %s", instanceID, batch.BatchID)
		}
		return nil
	}
	for _, item := range effects.InstanceUpdates {
		if err := validateInstance(item.SpaceID, item.InstanceID); err != nil {
			return err
		}
	}
	for _, item := range effects.Retries {
		if item == nil || strings.TrimSpace(item.InstanceID) == "" {
			continue
		}
		if err := validateInstance(item.SpaceID, item.InstanceID); err != nil {
			return err
		}
	}
	for _, item := range effects.WriteTargetUpdates {
		spaceID := strings.TrimSpace(item.SpaceID)
		if spaceID == "" {
			spaceID = strings.TrimSpace(batch.SpaceID)
		}
		if err := validateInstance(spaceID, item.InstanceID); err != nil {
			return err
		}
		var count int64
		if err := tx.Table("t_collector_instance_write_targets AS targets").
			Where("targets.c_space_id = ? AND targets.c_write_target_id = ? AND targets.c_instance_id = ? AND targets.c_dataset_id = ?", spaceID, strings.TrimSpace(item.WriteTargetID), strings.TrimSpace(item.InstanceID), strings.TrimSpace(item.DatasetID)).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("completion target %s is not attached to instance %s/dataset %s", item.WriteTargetID, item.InstanceID, item.DatasetID)
		}
	}
	return nil
}

func (r *FetchBatchRepository) ListDue(ctx context.Context, spaceID string, now time.Time, limit int) ([]domain.BatchInvocation, error) {
	if limit <= 0 {
		limit = 100
	}
	var batches []domain.BatchInvocation
	err := r.db.WithContext(ctx).
		Where(`c_space_id = ? AND c_status IN ? AND (
			(c_deadline_at IS NOT NULL AND c_deadline_at <= ?) OR
			(c_status = ? AND EXISTS (
				SELECT 1 FROM t_collector_timer_period_batches AS manifests
				WHERE manifests.c_space_id = t_collector_fetch_batches.c_space_id
				  AND manifests.c_batch_id = t_collector_fetch_batches.c_batch_id
				  AND manifests.c_claim_request_id = '' AND manifests.c_deadline_at <= ?
			))
		)`, spaceID, []domain.BatchStatus{domain.BatchStatusPlanned, domain.BatchStatusDispatched}, now.UTC(), domain.BatchStatusPlanned, now.UTC()).
		Order("c_deadline_at ASC").Limit(limit).Find(&batches).Error
	return batches, err
}

func (r *FetchBatchRepository) ListDuePrioritized(ctx context.Context, spaceID string, now time.Time, recentLimit, historicalLimit int) (recent, historical []domain.BatchInvocation, err error) {
	if recentLimit < 0 || historicalLimit < 0 {
		return nil, nil, gorm.ErrInvalidData
	}
	now = now.UTC()
	recentPredicate := "c_period_deadline_at IS NOT NULL AND c_period_deadline_at > ?"
	statuses := []domain.BatchStatus{domain.BatchStatusPlanned, domain.BatchStatusDispatched}
	recentGroups := make([][]domain.BatchInvocation, 0, len(statuses))
	historicalGroups := make([][]domain.BatchInvocation, 0, len(statuses))
	for _, status := range statuses {
		base := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status = ?", spaceID, status)
		if status == domain.BatchStatusPlanned {
			base = base.Where(`(
				(c_deadline_at IS NOT NULL AND c_deadline_at <= ?) OR
				EXISTS (
					SELECT 1 FROM t_collector_timer_period_batches AS manifests
					WHERE manifests.c_space_id = t_collector_fetch_batches.c_space_id
					  AND manifests.c_batch_id = t_collector_fetch_batches.c_batch_id
					  AND manifests.c_claim_request_id = '' AND manifests.c_deadline_at <= ?
				)
			)`, now, now)
		} else {
			base = base.Where("c_deadline_at IS NOT NULL AND c_deadline_at <= ?", now)
		}
		if recentLimit > 0 {
			var group []domain.BatchInvocation
			err = base.Session(&gorm.Session{}).Where(recentPredicate, now).
				Order("c_period_deadline_at ASC, c_deadline_at ASC, c_id ASC").Limit(recentLimit).Find(&group).Error
			if err != nil {
				return nil, nil, err
			}
			recentGroups = append(recentGroups, group)
		}
		if historicalLimit > 0 {
			var group []domain.BatchInvocation
			err = base.Session(&gorm.Session{}).Where("c_period_deadline_at IS NULL OR c_period_deadline_at <= ?", now).
				Order("c_deadline_at ASC, c_id ASC").Limit(historicalLimit).Find(&group).Error
			if err != nil {
				return nil, nil, err
			}
			historicalGroups = append(historicalGroups, group)
		}
	}
	recent = mergePrioritizedBatches(recentGroups, recentLimit, func(left, right domain.BatchInvocation) bool {
		if left.PeriodDeadlineAt != nil && right.PeriodDeadlineAt != nil && !left.PeriodDeadlineAt.Equal(*right.PeriodDeadlineAt) {
			return left.PeriodDeadlineAt.Before(*right.PeriodDeadlineAt)
		}
		if left.DeadlineAt != nil && right.DeadlineAt != nil && !left.DeadlineAt.Equal(*right.DeadlineAt) {
			return left.DeadlineAt.Before(*right.DeadlineAt)
		}
		return left.ID < right.ID
	})
	historical = mergePrioritizedBatches(historicalGroups, historicalLimit, func(left, right domain.BatchInvocation) bool {
		if left.DeadlineAt == nil || right.DeadlineAt == nil {
			if left.DeadlineAt != nil {
				return true
			}
			if right.DeadlineAt != nil {
				return false
			}
		} else if !left.DeadlineAt.Equal(*right.DeadlineAt) {
			return left.DeadlineAt.Before(*right.DeadlineAt)
		}
		return left.ID < right.ID
	})
	return recent, historical, nil
}

func mergePrioritizedBatches(groups [][]domain.BatchInvocation, limit int, less func(domain.BatchInvocation, domain.BatchInvocation) bool) []domain.BatchInvocation {
	if limit <= 0 {
		return nil
	}
	merged := make([]domain.BatchInvocation, 0, limit*len(groups))
	for _, group := range groups {
		merged = append(merged, group...)
	}
	sort.Slice(merged, func(left, right int) bool { return less(merged[left], merged[right]) })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// HasActiveTask reports planned/dispatched batches that can still write for
// one CollectionTask. Shared batches are discovered through
// batch_items -> write_targets rather than only BatchInvocation.TaskID.
func (r *FetchBatchRepository) HasActiveTask(ctx context.Context, spaceID, taskID string, kinds ...domain.BatchKind) (bool, error) {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if taskID == "" {
		return false, nil
	}
	query := r.db.WithContext(ctx).
		Table("t_collector_fetch_batches AS batches").
		Joins(`LEFT JOIN t_collector_fetch_batch_items AS items ON items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id`).
		Joins(`LEFT JOIN t_collector_instance_write_targets AS targets ON targets.c_space_id = items.c_space_id AND targets.c_instance_id = items.c_instance_id`).
		Where("batches.c_space_id = ? AND batches.c_status IN ? AND targets.c_task_id = ?", spaceID, []domain.BatchStatus{domain.BatchStatusPlanned, domain.BatchStatusDispatched}, taskID)
	if len(kinds) > 0 {
		query = query.Where("batches.c_batch_kind IN ?", kinds)
	}
	var count int64
	if err := query.Distinct("batches.c_batch_id").Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *FetchBatchRepository) Cleanup(ctx context.Context, terminalBefore time.Time) error {
	return r.cleanup(ctx, "", terminalBefore, maxFetchBatchCleanupItemRows, maxFetchBatchCleanupBatches)
}

// CleanupSpace gives each configured Space an independent bounded cleanup
// page so a high-volume Space cannot consume another Space's maintenance turn.
func (r *FetchBatchRepository) CleanupSpace(ctx context.Context, spaceID string, terminalBefore time.Time, itemBudget, batchBudget int) error {
	_, _, err := r.CleanupSpaceWithCounts(ctx, spaceID, terminalBefore, itemBudget, batchBudget)
	return err
}

// CleanupSpaceWithCounts returns only committed item and batch deletes.
func (r *FetchBatchRepository) CleanupSpaceWithCounts(ctx context.Context, spaceID string, terminalBefore time.Time, itemBudget, batchBudget int) (int64, int64, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, 0, fmt.Errorf("space_id is required for scoped batch cleanup")
	}
	return r.cleanupWithCounts(ctx, spaceID, terminalBefore, itemBudget, batchBudget)
}

func (r *FetchBatchRepository) cleanup(ctx context.Context, spaceID string, terminalBefore time.Time, itemBudget, batchBudget int) error {
	_, _, err := r.cleanupWithCounts(ctx, spaceID, terminalBefore, itemBudget, batchBudget)
	return err
}

func (r *FetchBatchRepository) cleanupWithCounts(ctx context.Context, spaceID string, terminalBefore time.Time, itemBudget, batchBudget int) (int64, int64, error) {
	itemBudget = min(max(0, itemBudget), maxFetchBatchCleanupItemRows)
	batchBudget = min(max(0, batchBudget), maxFetchBatchCleanupBatches)
	var itemsDeleted, batchesDeleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before := terminalBefore.UTC()
		statuses := []struct {
			status domain.BatchStatus
			before time.Time
		}{
			{status: domain.BatchStatusSucceeded, before: before},
			{status: domain.BatchStatusPartialFailed, before: before},
			{status: domain.BatchStatusFailed, before: before},
			{status: domain.BatchStatusTimedOut, before: before},
		}
		remainingItems := itemBudget
		remainingBatches := batchBudget
		for _, pass := range []int{0, 1} {
			for index, candidate := range statuses {
				remainingStatuses := len(statuses) - index
				itemLimit := remainingItems
				if pass == 0 {
					itemLimit = (remainingItems + remainingStatuses - 1) / remainingStatuses
				}
				itemQuery := fetchBatchCleanupItemsPageSQL
				itemArgs := []any{candidate.status, candidate.before, itemLimit}
				if spaceID != "" {
					itemQuery = fetchBatchCleanupItemsPageSpaceSQL
					itemArgs = []any{spaceID, candidate.status, candidate.before, itemLimit}
				}
				itemResult := tx.Exec(`DELETE FROM t_collector_fetch_batch_items WHERE rowid IN (`+itemQuery+`)`, itemArgs...)
				if itemResult.Error != nil {
					return itemResult.Error
				}
				remainingItems -= int(itemResult.RowsAffected)
				itemsDeleted += itemResult.RowsAffected

				batchLimit := remainingBatches
				if pass == 0 {
					batchLimit = (remainingBatches + remainingStatuses - 1) / remainingStatuses
				}
				batchQuery := fetchBatchCleanupBatchesPageSQL
				batchArgs := []any{candidate.status, candidate.before, batchLimit}
				if spaceID != "" {
					batchQuery = fetchBatchCleanupBatchesPageSpaceSQL
					batchArgs = []any{spaceID, candidate.status, candidate.before, batchLimit}
				}
				batchResult := tx.Exec(`DELETE FROM t_collector_fetch_batches WHERE c_id IN (`+batchQuery+`)`, batchArgs...)
				if batchResult.Error != nil {
					return batchResult.Error
				}
				remainingBatches -= int(batchResult.RowsAffected)
				batchesDeleted += batchResult.RowsAffected
			}
			if err := markItemlessTerminalBatchesCleaned(tx, statuses, batchBudget, spaceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return itemsDeleted, batchesDeleted, nil
}

func markItemlessTerminalBatchesCleaned(tx *gorm.DB, statuses []struct {
	status domain.BatchStatus
	before time.Time
}, limit int, spaceID string) error {
	for _, candidate := range statuses {
		query := `UPDATE t_collector_fetch_batches SET c_items_cleaned = 1 WHERE c_id IN (
			SELECT batches.c_id
			FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_items_cleanup
			WHERE batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ?
			  AND batches.c_items_cleaned = 0
			  AND NOT (batches.c_status = 'timed_out' AND batches.c_late_completion = 0 AND EXISTS (
				SELECT 1 FROM t_collector_timer_period_batches AS manifests
				WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
			  ))
			  AND NOT EXISTS (
				SELECT 1 FROM t_collector_fetch_batch_items AS items
				WHERE items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
			  )
			ORDER BY batches.c_completed_at, batches.c_id
			LIMIT ?
		)`
		args := []any{candidate.status, candidate.before, limit}
		if spaceID != "" {
			query = `UPDATE t_collector_fetch_batches SET c_items_cleaned = 1 WHERE c_id IN (
			SELECT batches.c_id
			FROM t_collector_fetch_batches AS batches INDEXED BY idx_collector_fetch_batch_terminal_items_cleanup_space
			WHERE batches.c_space_id = ? AND batches.c_status = ? AND batches.c_completed_at IS NOT NULL AND batches.c_completed_at < ?
			  AND batches.c_items_cleaned = 0
			  AND NOT (batches.c_status = 'timed_out' AND batches.c_late_completion = 0 AND EXISTS (
				SELECT 1 FROM t_collector_timer_period_batches AS manifests
				WHERE manifests.c_space_id = batches.c_space_id AND manifests.c_batch_id = batches.c_batch_id
			  ))
			  AND NOT EXISTS (
				SELECT 1 FROM t_collector_fetch_batch_items AS items
				WHERE items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id
			  )
			ORDER BY batches.c_completed_at, batches.c_id
			LIMIT ?
		)`
			args = []any{spaceID, candidate.status, candidate.before, limit}
		}
		if err := tx.Exec(query, args...).Error; err != nil {
			return err
		}
	}
	return nil
}
