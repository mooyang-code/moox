package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxTimerBatchItems       = 40
	maxTimerBatchRequestSize = 256 * 1024
	maxTimerClaimAttempts    = 8
)

var errTimerBatchClaimCAS = errors.New("timer period batch claim compare-and-swap lost")
var errTimerPeriodBatchCreateCAS = errors.New("timer period batch creation compare-and-swap lost")

type TimerPeriodBatchRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewTimerPeriodBatchRepository(db *gorm.DB) *TimerPeriodBatchRepository {
	return &TimerPeriodBatchRepository{db: db, now: time.Now}
}

type TimerPeriodBatchPlan struct {
	Manifest            *domain.TimerPeriodBatch
	Batch               *domain.BatchInvocation
	Instances           []domain.TaskInstance
	Targets             []domain.WriteTarget
	OwnerRunCutoff      time.Time
	OwnerTaskModifyTime time.Time
}

type TimerPeriodBatchClaimInput struct {
	SpaceID             string
	FunctionName        string
	RequestID           string
	GroupID             uint32
	GroupCount          uint32
	BindingHash         string
	TickTime            time.Time
	NodeID              string
	Region              string
	CompletionTimeout   time.Duration
	ValidateRequestJSON func(batchID string, requestJSON []byte) error
}

type TimerPeriodBatchClaimResult struct {
	Claimed          bool
	BatchID          string
	RequestJSON      []byte
	PeriodDeadlineAt time.Time
}

// Create installs a manifest, its owner batch, and its frozen membership in a
// single transaction. A duplicate period is an immutable read-only reuse: no
// new Run or tag expansion can append members after the first winner.
func (r *TimerPeriodBatchRepository) Create(ctx context.Context, plan TimerPeriodBatchPlan) (bool, error) {
	created, err := r.CreateMany(ctx, []TimerPeriodBatchPlan{plan})
	return created > 0, err
}

// CreateMany atomically installs every active group for one frozen period.
// Duplicate period creation is read-only, so a later assignment refresh cannot
// fill gaps or repartition membership after the first winner.
func (r *TimerPeriodBatchRepository) CreateMany(ctx context.Context, plans []TimerPeriodBatchPlan) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("timer period batch repository is not initialized")
	}
	if len(plans) == 0 {
		return 0, fmt.Errorf("timer period batch plan set is empty")
	}
	prepared := make([]TimerPeriodBatchPlan, len(plans))
	seenGroups := make(map[uint32]struct{}, len(plans))
	seenInstances := make(map[string]struct{})
	seenSeries := make(map[uint32]struct{})
	var first *domain.TimerPeriodBatch
	var ownerRunCutoff, ownerTaskModifyTime time.Time
	for index, plan := range plans {
		if plan.Manifest == nil || plan.Batch == nil {
			return 0, gorm.ErrInvalidData
		}
		manifest := *plan.Manifest
		batch := *plan.Batch
		prepared[index] = plan
		prepared[index].Manifest = &manifest
		prepared[index].Batch = &batch
		m, b := prepared[index].Manifest, prepared[index].Batch
		if strings.TrimSpace(m.Key) == "" || strings.TrimSpace(m.SpaceID) == "" ||
			strings.TrimSpace(m.DatasetID) == "" || strings.TrimSpace(m.Frequency) == "" || m.PeriodTime.IsZero() ||
			strings.TrimSpace(m.TaskID) == "" || strings.TrimSpace(m.FirstRunID) == "" || strings.TrimSpace(m.SeriesHash) == "" ||
			m.ExpectedCount == 0 || m.GroupCount == 0 || m.GroupID >= m.GroupCount || strings.TrimSpace(m.BindingHash) == "" ||
			strings.TrimSpace(m.BatchID) == "" || strings.TrimSpace(m.FunctionName) == "" || m.DeadlineAt.IsZero() ||
			b.SpaceID != m.SpaceID || b.BatchID != m.BatchID || b.Status != domain.BatchStatusPlanned {
			return 0, gorm.ErrInvalidData
		}
		if first == nil {
			first = m
			ownerRunCutoff = plan.OwnerRunCutoff.UTC()
			ownerTaskModifyTime = plan.OwnerTaskModifyTime.UTC()
		} else if m.SpaceID != first.SpaceID || m.DatasetID != first.DatasetID || m.Frequency != first.Frequency ||
			!m.PeriodTime.UTC().Equal(first.PeriodTime.UTC()) || m.TaskID != first.TaskID || m.FirstRunID != first.FirstRunID ||
			m.SeriesHash != first.SeriesHash || m.ExpectedCount != first.ExpectedCount || m.GroupCount != first.GroupCount || m.RouteVersion != first.RouteVersion ||
			!plan.OwnerRunCutoff.UTC().Equal(ownerRunCutoff) || !plan.OwnerTaskModifyTime.UTC().Equal(ownerTaskModifyTime) {
			return 0, fmt.Errorf("timer period batch set mixes immutable period or owner identity")
		}
		if _, exists := seenGroups[m.GroupID]; exists {
			return 0, fmt.Errorf("timer period batch set has duplicate group_id %d", m.GroupID)
		}
		seenGroups[m.GroupID] = struct{}{}
		if len(plan.Instances) == 0 || len(plan.Instances) > maxTimerBatchItems || len(plan.Targets) != len(plan.Instances) {
			return 0, fmt.Errorf("timer period plan membership is empty, exceeds item limit, or lacks one target per instance")
		}
		if len(b.RequestJSON) == 0 || len(b.RequestJSON) > maxTimerBatchRequestSize {
			return 0, fmt.Errorf("timer period request_json is empty or exceeds %d bytes", maxTimerBatchRequestSize)
		}
		if b.PlannedCount != len(plan.Instances) {
			return 0, fmt.Errorf("timer period planned_count does not match frozen instance membership")
		}
		targetInstances := make(map[string]struct{}, len(plan.Targets))
		for _, instance := range plan.Instances {
			if strings.TrimSpace(instance.InstanceID) == "" || instance.SpaceID != m.SpaceID {
				return 0, gorm.ErrInvalidData
			}
			if _, exists := seenInstances[instance.InstanceID]; exists {
				return 0, fmt.Errorf("timer period batch set has duplicate instance_id %s", instance.InstanceID)
			}
			seenInstances[instance.InstanceID] = struct{}{}
		}
		for _, target := range plan.Targets {
			if target.ID == "" || target.SpaceID != m.SpaceID || target.TaskID != m.TaskID || target.DatasetID != m.DatasetID ||
				target.SeriesHash != m.SeriesHash || target.ExpectedCount != m.ExpectedCount {
				return 0, gorm.ErrInvalidData
			}
			if _, exists := targetInstances[target.InstanceID]; exists {
				return 0, fmt.Errorf("timer period plan has duplicate WriteTarget instance_id %s", target.InstanceID)
			}
			targetInstances[target.InstanceID] = struct{}{}
			if _, exists := seenSeries[target.SeriesIndex]; exists {
				return 0, fmt.Errorf("timer period batch set assigns series_index %d more than once", target.SeriesIndex)
			}
			seenSeries[target.SeriesIndex] = struct{}{}
		}
		if len(targetInstances) != len(plan.Instances) {
			return 0, fmt.Errorf("timer period plan target membership differs from instances")
		}
		for instanceID := range targetInstances {
			found := false
			for _, instance := range plan.Instances {
				if instance.InstanceID == instanceID {
					found = true
					break
				}
			}
			if !found {
				return 0, fmt.Errorf("timer period plan target %s has no instance", instanceID)
			}
		}
	}
	if first == nil || len(seenSeries) != int(first.ExpectedCount) {
		return 0, fmt.Errorf("timer period batch set does not cover every frozen series exactly once")
	}
	for index := uint32(0); index < first.ExpectedCount; index++ {
		if _, exists := seenSeries[index]; !exists {
			return 0, fmt.Errorf("timer period batch set is missing frozen series_index %d", index)
		}
	}
	created := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		ownerQuery := tx.Model(&domain.CollectionTask{}).Where("c_space_id = ? AND c_task_id = ? AND c_enabled = 1", first.SpaceID, first.TaskID)
		if !ownerRunCutoff.IsZero() {
			ownerQuery = ownerQuery.Where("c_ctime <= ? AND c_mtime <= ?", ownerRunCutoff, ownerRunCutoff)
		}
		if !ownerTaskModifyTime.IsZero() {
			ownerQuery = ownerQuery.Where("c_mtime = ?", ownerTaskModifyTime)
		}
		var enabledOwner int64
		if err := ownerQuery.Count(&enabledOwner).Error; err != nil {
			return err
		}
		if enabledOwner == 0 {
			return nil
		}
		var existingCount int64
		if err := tx.Model(&domain.TimerPeriodBatch{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", first.SpaceID, first.DatasetID, first.Frequency, first.PeriodTime.UTC()).
			Count(&existingCount).Error; err != nil {
			return err
		}
		if existingCount != 0 {
			return nil
		}
		for _, plan := range prepared {
			m := plan.Manifest
			m.PeriodTime = m.PeriodTime.UTC()
			m.DeadlineAt = m.DeadlineAt.UTC()
			if m.CreateTime.IsZero() {
				m.CreateTime = now
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(m)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				var existing domain.TimerPeriodBatch
				err := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?",
					m.SpaceID, m.DatasetID, m.Frequency, m.PeriodTime).First(&existing).Error
				if err == nil {
					return errTimerPeriodBatchCreateCAS
				}
				return fmt.Errorf("timer period batch key conflicts with another period: %w", result.Error)
			}
		}
		for _, plan := range prepared {
			m, b := plan.Manifest, plan.Batch
			if err := NewTaskInstanceRepository(tx).upsertManyTx(ctx, tx, plan.Instances); err != nil {
				return err
			}
			for _, target := range plan.Targets {
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_instance_id"}, {Name: "c_task_id"}},
					DoUpdates: clause.AssignmentColumns([]string{"c_dataset_id", "c_view_id", "c_output_fields_json", "c_series_index", "c_series_hash", "c_expected_count", "c_mtime"}),
				}).Create(&target).Error; err != nil {
					return err
				}
			}
			if b.PlannedAt == nil {
				b.PlannedAt = &now
			}
			if b.CreateTime.IsZero() {
				b.CreateTime = now
			}
			b.ModifyTime = now
			if err := tx.Create(b).Error; err != nil {
				return err
			}
			for _, instance := range plan.Instances {
				if err := tx.Exec(`INSERT INTO t_collector_fetch_batch_items(c_space_id,c_batch_id,c_instance_id) VALUES(?,?,?) ON CONFLICT(c_space_id,c_batch_id,c_instance_id) DO NOTHING`, m.SpaceID, m.BatchID, instance.InstanceID).Error; err != nil {
					return err
				}
			}
		}
		created = len(prepared)
		return nil
	})
	if errors.Is(err, errTimerPeriodBatchCreateCAS) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return created, err
}

func (r *TimerPeriodBatchRepository) ListByPeriod(ctx context.Context, key domain.PeriodKey) ([]domain.TimerPeriodBatch, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("timer period batch repository is not initialized")
	}
	var manifests []domain.TimerPeriodBatch
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?",
		strings.TrimSpace(key.SpaceID), strings.TrimSpace(key.DatasetID), strings.TrimSpace(key.Frequency), key.PeriodTime.UTC()).
		Order("c_shard_index ASC").Find(&manifests).Error
	return manifests, err
}

func (r *TimerPeriodBatchRepository) GetByPeriod(ctx context.Context, key domain.PeriodKey, shardIndex uint32) (*domain.TimerPeriodBatch, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("timer period batch repository is not initialized")
	}
	var manifest domain.TimerPeriodBatch
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ? AND c_shard_index = ?",
		strings.TrimSpace(key.SpaceID), strings.TrimSpace(key.DatasetID), strings.TrimSpace(key.Frequency), key.PeriodTime.UTC(), shardIndex).First(&manifest).Error
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

// CancelStaleUnclaimed closes only still-planned Timer batches whose owning
// task has been disabled or modified since the manifest was frozen. Claimed
// work keeps its original owner and completion lifecycle.
func (r *TimerPeriodBatchRepository) CancelStaleUnclaimed(ctx context.Context, spaceID string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("timer period batch repository is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required")
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Exec(`
UPDATE t_collector_fetch_batches
SET c_status = ?, c_error_summary = ?, c_completed_at = ?, c_mtime = ?
WHERE c_space_id = ? AND c_status = ?
  AND EXISTS (
      SELECT 1 FROM t_collector_timer_period_batches AS manifest
      WHERE manifest.c_space_id = t_collector_fetch_batches.c_space_id
        AND manifest.c_batch_id = t_collector_fetch_batches.c_batch_id
        AND manifest.c_claim_request_id = ''
        AND NOT EXISTS (
            SELECT 1 FROM t_collector_tasks AS task
            WHERE task.c_space_id = manifest.c_space_id AND task.c_task_id = manifest.c_task_id
              AND task.c_enabled = 1 AND task.c_mtime <= manifest.c_ctime
        )
  )`, domain.BatchStatusFailed, "timer batch canceled because its task changed before claim", now, now,
		spaceID, domain.BatchStatusPlanned)
	return result.RowsAffected, result.Error
}

// Claim binds one Timer request to the oldest matching planned manifest. A
// request ID is looked up before candidate selection, making retries after a
// lost response stable and preventing the same ID from taking another period.
func (r *TimerPeriodBatchRepository) Claim(ctx context.Context, input TimerPeriodBatchClaimInput) (TimerPeriodBatchClaimResult, error) {
	if r == nil || r.db == nil {
		return TimerPeriodBatchClaimResult{}, fmt.Errorf("timer period batch repository is not initialized")
	}
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.FunctionName = strings.TrimSpace(input.FunctionName)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.BindingHash = strings.TrimSpace(input.BindingHash)
	if input.SpaceID == "" || input.FunctionName == "" || input.RequestID == "" || input.BindingHash == "" ||
		input.GroupCount == 0 || input.GroupID >= input.GroupCount || input.TickTime.IsZero() || input.CompletionTimeout <= 0 {
		return TimerPeriodBatchClaimResult{}, gorm.ErrInvalidData
	}
	input.TickTime = input.TickTime.UTC()
	for attempt := 0; attempt < maxTimerClaimAttempts; attempt++ {
		result, err := r.claimOnce(ctx, input)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, errTimerBatchClaimCAS) && !isSQLiteBusyError(err) {
			return TimerPeriodBatchClaimResult{}, err
		}
		if err := waitTimerClaimRetry(ctx, attempt); err != nil {
			return TimerPeriodBatchClaimResult{}, err
		}
	}
	return TimerPeriodBatchClaimResult{}, fmt.Errorf("timer period claim exceeded bounded CAS retries")
}

func (r *TimerPeriodBatchRepository) claimOnce(ctx context.Context, input TimerPeriodBatchClaimInput) (TimerPeriodBatchClaimResult, error) {
	var output TimerPeriodBatchClaimResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		selectAt := r.currentTime()
		var replay domain.TimerPeriodBatch
		replayErr := tx.Where("c_space_id = ? AND c_function_name = ? AND c_claim_request_id = ?", input.SpaceID, input.FunctionName, input.RequestID).First(&replay).Error
		if replayErr == nil {
			if replay.GroupID != input.GroupID || replay.GroupCount != input.GroupCount || replay.BindingHash != input.BindingHash {
				return fmt.Errorf("timer request ID is already bound to a different group or binding")
			}
			var batch domain.BatchInvocation
			if err := tx.Where("c_space_id = ? AND c_batch_id = ?", replay.SpaceID, replay.BatchID).First(&batch).Error; err != nil {
				return err
			}
			if input.ValidateRequestJSON != nil {
				if err := input.ValidateRequestJSON(batch.BatchID, []byte(batch.RequestJSON)); err != nil {
					return fmt.Errorf("validate persisted timer request_json: %w", err)
				}
			}
			if batch.Status.Terminal() {
				return nil
			}
			output = TimerPeriodBatchClaimResult{Claimed: true, BatchID: replay.BatchID, RequestJSON: []byte(batch.RequestJSON), PeriodDeadlineAt: replay.DeadlineAt.UTC()}
			return nil
		}
		if !errors.Is(replayErr, gorm.ErrRecordNotFound) {
			return replayErr
		}

		var candidates []domain.TimerPeriodBatch
		err := tx.Table("t_collector_timer_period_batches AS manifest").
			Select("manifest.*").
			Joins("JOIN t_collector_fetch_batches AS batches ON batches.c_space_id = manifest.c_space_id AND batches.c_batch_id = manifest.c_batch_id").
			Joins("JOIN t_collector_tasks AS tasks ON tasks.c_space_id = manifest.c_space_id AND tasks.c_task_id = manifest.c_task_id").
			Where("manifest.c_space_id = ? AND manifest.c_function_name = ? AND manifest.c_group_id = ? AND manifest.c_group_count = ? AND manifest.c_binding_hash = ? AND manifest.c_claim_request_id = '' AND manifest.c_deadline_at > ? AND batches.c_status = ? AND tasks.c_enabled = 1 AND tasks.c_mtime <= manifest.c_ctime",
				input.SpaceID, input.FunctionName, input.GroupID, input.GroupCount, input.BindingHash, selectAt, domain.BatchStatusPlanned).
			Order("manifest.c_period_time ASC, manifest.c_dataset_id ASC, manifest.c_frequency ASC, manifest.c_shard_index ASC").
			Limit(1).Find(&candidates).Error
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		candidate := candidates[0]
		var batch domain.BatchInvocation
		if err := tx.Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", candidate.SpaceID, candidate.BatchID, domain.BatchStatusPlanned).First(&batch).Error; err != nil {
			return err
		}
		if input.ValidateRequestJSON != nil {
			if err := input.ValidateRequestJSON(batch.BatchID, []byte(batch.RequestJSON)); err != nil {
				return fmt.Errorf("validate persisted timer request_json: %w", err)
			}
		}
		requestJSON, err := timerRequestWithID(batch.RequestJSON, input.RequestID)
		if err != nil {
			return err
		}
		if len(requestJSON) == 0 || len(requestJSON) > maxTimerBatchRequestSize {
			return fmt.Errorf("timer period request_json exceeds %d bytes", maxTimerBatchRequestSize)
		}
		now := r.currentTime()
		manifestResult := tx.Model(&domain.TimerPeriodBatch{}).
			Where("c_key = ? AND c_claim_request_id = '' AND c_deadline_at > ?", candidate.Key, now).
			Updates(map[string]any{"c_claim_request_id": input.RequestID, "c_claimed_at": now})
		if manifestResult.Error != nil {
			return manifestResult.Error
		}
		if manifestResult.RowsAffected != 1 {
			return errTimerBatchClaimCAS
		}
		batchDeadline := now.Add(input.CompletionTimeout)
		batchResult := tx.Model(&domain.BatchInvocation{}).
			Where("c_space_id = ? AND c_batch_id = ? AND c_status = ?", candidate.SpaceID, candidate.BatchID, domain.BatchStatusPlanned).
			Updates(map[string]any{"c_status": domain.BatchStatusDispatched, "c_request_id": input.RequestID, "c_request_json": string(requestJSON),
				"c_dispatched_at": now, "c_deadline_at": batchDeadline, "c_mtime": now})
		if batchResult.Error != nil {
			return batchResult.Error
		}
		if batchResult.RowsAffected != 1 {
			return errTimerBatchClaimCAS
		}
		output = TimerPeriodBatchClaimResult{Claimed: true, BatchID: candidate.BatchID, RequestJSON: requestJSON, PeriodDeadlineAt: candidate.DeadlineAt.UTC()}
		return nil
	})
	return output, err
}

func (r *TimerPeriodBatchRepository) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func timerRequestWithID(raw, requestID string) ([]byte, error) {
	var bounds struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &bounds); err != nil {
		return nil, fmt.Errorf("decode persisted timer request items: %w", err)
	}
	if len(bounds.Items) == 0 || len(bounds.Items) > maxTimerBatchItems {
		return nil, fmt.Errorf("persisted timer batch item count %d is outside 1..%d", len(bounds.Items), maxTimerBatchItems)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return nil, fmt.Errorf("decode persisted timer request_json: %w", err)
	}
	if request == nil {
		return nil, fmt.Errorf("persisted timer request_json must be an object")
	}
	encodedID, _ := json.Marshal(requestID)
	request["request_id"] = encodedID
	return json.Marshal(request)
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "busy snapshot")
}

func waitTimerClaimRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
