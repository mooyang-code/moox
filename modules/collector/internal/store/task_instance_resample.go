package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

var ErrResampleBackfillConflict = errors.New("another resample backfill is active")

func timePtr(value time.Time) *time.Time { return &value }

type ResampleTaskClaim struct {
	Instance domain.TaskInstance
	Result   domain.ResampleTaskResult
}

// InitializeResampleTask sets the first realtime cursor without resetting a
// task that already has progress. It is safe to call on every scheduler tick.
func (r *TaskInstanceRepository) InitializeResampleTask(ctx context.Context, spaceID, taskID string, nextBucket time.Time) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(taskID) == "" || nextBucket.IsZero() {
		return fmt.Errorf("space_id, task_id and next_bucket are required")
	}
	var instance domain.TaskInstance
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ? AND c_is_deleted = ?", spaceID, taskID, "kline_resample", false).First(&instance).Error; err != nil {
		return err
	}
	result, err := domain.ParseResampleTaskResult(instance.Result)
	if err != nil {
		return err
	}
	if result.RealtimeNextBucket != nil {
		return nil
	}
	result.RealtimeNextBucket = timePtr(nextBucket.UTC())
	result.StateVersion++
	encoded, err := result.Marshal()
	if err != nil {
		return err
	}
	updated, err := updateResampleResultCAS(r.db.WithContext(ctx), spaceID, taskID, instance.Result, result.StateVersion-1, encoded, domain.InstanceStatusPending)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}
	return nil
}

// ClaimDueResampleTasks claims due realtime or backfill work. Repair buckets
// are selected externally and claimed with ClaimResampleTask.
func (r *TaskInstanceRepository) ClaimDueResampleTasks(
	ctx context.Context,
	now time.Time,
	origin domain.ResampleTaskOrigin,
	limit int,
	leaseDuration time.Duration,
) ([]ResampleTaskClaim, error) {
	return r.claimDueResampleTasks(ctx, "", now, origin, limit, leaseDuration, false)
}

// ClaimDueResampleTasksInSpace limits claims to one Collector space. The
// compatibility wrapper above is retained for repository callers that need a
// whole-database maintenance sweep.
func (r *TaskInstanceRepository) ClaimDueResampleTasksInSpace(
	ctx context.Context,
	spaceID string,
	now time.Time,
	origin domain.ResampleTaskOrigin,
	limit int,
	leaseDuration time.Duration,
) ([]ResampleTaskClaim, error) {
	return r.claimDueResampleTasks(ctx, strings.TrimSpace(spaceID), now, origin, limit, leaseDuration, true)
}

func (r *TaskInstanceRepository) claimDueResampleTasks(
	ctx context.Context,
	spaceID string,
	now time.Time,
	origin domain.ResampleTaskOrigin,
	limit int,
	leaseDuration time.Duration,
	requireReadyRule bool,
) ([]ResampleTaskClaim, error) {
	if !origin.Valid() {
		return nil, fmt.Errorf("invalid resample origin: %s", origin)
	}
	if limit <= 0 || limit > maxPageSize {
		return nil, fmt.Errorf("claim limit must be between 1 and %d", maxPageSize)
	}
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}
	now = now.UTC()
	claims := make([]ResampleTaskClaim, 0, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instances []domain.TaskInstance
		query := tx.Where("t_collector_task_instances.c_data_type = ? AND t_collector_task_instances.c_is_deleted = ?", "kline_resample", false)
		if requireReadyRule {
			query = query.Where(`EXISTS (SELECT 1 FROM t_collector_task_rules rules
				WHERE rules.c_space_id = t_collector_task_instances.c_space_id
				AND rules.c_rule_id = t_collector_task_instances.c_rule_id
				AND rules.c_data_type = 'kline_resample'
				AND rules.c_enabled = 1
				AND rules.c_prepare_state = 'ready')`)
		}
		if strings.TrimSpace(spaceID) != "" {
			query = query.Where("t_collector_task_instances.c_space_id = ?", strings.TrimSpace(spaceID))
		}
		if err := query.Order("t_collector_task_instances.c_mtime ASC, t_collector_task_instances.c_id ASC").Find(&instances).Error; err != nil {
			return err
		}
		for _, instance := range instances {
			if len(claims) >= limit {
				break
			}
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err != nil {
				if markErr := markCorruptResampleResult(tx, instance, err); markErr != nil {
					return markErr
				}
				log.ErrorContextf(ctx, "[Collector] invalid resample task result task=%s: %v", instance.TaskID, err)
				continue
			}
			bucket, due := dueResampleBucket(result, origin, now)
			if !due {
				continue
			}
			if origin == domain.ResampleOriginRealtime {
				target, parseErr := parseTaskFrequency(instance.Frequency)
				if parseErr != nil {
					continue
				}
				settleDelay := time.Duration(0)
				if params, paramsErr := domain.ParseCollectParams(instance.TaskParams, instance.Provider, instance.MarketType, "kline_resample"); paramsErr == nil {
					settleDelay = params.SettleDelay()
				}
				if bucket.Add(target).Add(settleDelay).After(now) {
					continue
				}
			}
			claim, claimed, err := claimResampleTaskTx(tx, instance, result, instance.Result, origin, bucket, now, leaseDuration)
			if err != nil {
				return err
			}
			if claimed {
				claims = append(claims, claim)
			}
		}
		return nil
	})
	return claims, err
}

func parseTaskFrequency(raw string) (time.Duration, error) {
	// Keep the store independent of the resample worker package while applying
	// the same fixed-minute closure rule to realtime claims.
	if strings.TrimSpace(raw) == "" {
		return 0, errors.New("task frequency is required")
	}
	value := strings.TrimSpace(raw)
	unit := value[len(value)-1]
	count, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil || count <= 0 {
		return 0, errors.New("task frequency is invalid")
	}
	var multiplier time.Duration
	switch unit {
	case 'm':
		multiplier = time.Minute
	case 'h', 'H':
		multiplier = time.Hour
	case 'd', 'D':
		multiplier = 24 * time.Hour
	default:
		return 0, errors.New("task frequency is invalid")
	}
	return time.Duration(count) * multiplier, nil
}

// ClaimResampleTask claims one scanner-selected bucket with a state-version CAS.
func (r *TaskInstanceRepository) ClaimResampleTask(
	ctx context.Context,
	spaceID, taskID string,
	expectedStateVersion int64,
	origin domain.ResampleTaskOrigin,
	bucket, now time.Time,
	leaseDuration time.Duration,
) (ResampleTaskClaim, bool, error) {
	if !origin.Valid() || bucket.IsZero() || leaseDuration <= 0 {
		return ResampleTaskClaim{}, false, fmt.Errorf("valid origin, bucket and lease duration are required")
	}
	var claim ResampleTaskClaim
	claimed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		instance, result, raw, err := loadResampleTask(tx, spaceID, taskID)
		if err != nil {
			return err
		}
		if result.StateVersion != expectedStateVersion || (result.State != domain.ResampleTaskStateIdle && result.State != domain.ResampleTaskStateWaitingSource) {
			return nil
		}
		claim, claimed, err = claimResampleTaskTx(tx, instance, result, raw, origin, bucket.UTC(), now.UTC(), leaseDuration)
		return err
	})
	return claim, claimed, err
}

func dueResampleBucket(result domain.ResampleTaskResult, origin domain.ResampleTaskOrigin, now time.Time) (time.Time, bool) {
	if result.State == domain.ResampleTaskStateWaitingSource {
		return timeValue(result.ActiveBucket), result.ActiveOrigin == origin && result.ActiveBucket != nil && result.NextRetryAt != nil && !result.NextRetryAt.After(now)
	}
	if result.State != domain.ResampleTaskStateIdle {
		return time.Time{}, false
	}
	switch origin {
	case domain.ResampleOriginRealtime:
		return timeValue(result.RealtimeNextBucket), result.RealtimeNextBucket != nil && !result.RealtimeNextBucket.After(now)
	case domain.ResampleOriginBackfill:
		backfill := result.Backfill
		return func() (time.Time, bool) {
			if backfill == nil || (backfill.State != domain.ResampleBackfillRunning && backfill.State != domain.ResampleBackfillWaitingSource) {
				return time.Time{}, false
			}
			return backfill.NextBucket, backfill.NextBucket.Before(backfill.End)
		}()
	default:
		return time.Time{}, false
	}
}

func claimResampleTaskTx(tx *gorm.DB, instance domain.TaskInstance, result domain.ResampleTaskResult, raw string, origin domain.ResampleTaskOrigin, bucket, now time.Time, leaseDuration time.Duration) (ResampleTaskClaim, bool, error) {
	lease := now.Add(leaseDuration).UTC()
	bucket = bucket.UTC()
	result.State = domain.ResampleTaskStateRunning
	result.StateVersion++
	result.ActiveOrigin = origin
	result.ActiveBucket = &bucket
	result.LeaseUntil = &lease
	result.NextRetryAt = nil
	result.LastError = ""
	encoded, err := result.Marshal()
	if err != nil {
		return ResampleTaskClaim{}, false, err
	}
	updated, err := updateResampleResultCAS(tx, instance.SpaceID, instance.TaskID, raw, result.StateVersion-1, encoded, domain.InstanceStatusPending)
	if err != nil || !updated {
		return ResampleTaskClaim{}, updated, err
	}
	instance.Result = encoded
	return ResampleTaskClaim{Instance: instance, Result: result}, true, nil
}

func (r *TaskInstanceRepository) CompleteResampleTask(ctx context.Context, spaceID, taskID string, expectedStateVersion int64, bucket, nextBucket time.Time, inputHash string) (bool, error) {
	return r.updateResampleTask(ctx, spaceID, taskID, expectedStateVersion, func(result *domain.ResampleTaskResult) error {
		if result.State != domain.ResampleTaskStateRunning || result.ActiveBucket == nil || !result.ActiveBucket.Equal(bucket) {
			return errResampleCASMismatch
		}
		origin := result.ActiveOrigin
		completed := bucket.UTC()
		result.State = domain.ResampleTaskStateIdle
		result.ActiveOrigin = ""
		result.ActiveBucket = nil
		result.LeaseUntil = nil
		result.Attempt = 0
		result.NextRetryAt = nil
		result.LastError = ""
		result.LastInputHash = strings.TrimSpace(inputHash)
		if result.LastSuccessBucket == nil || completed.After(*result.LastSuccessBucket) {
			result.LastSuccessBucket = &completed
		}
		switch origin {
		case domain.ResampleOriginRealtime:
			next := nextBucket.UTC()
			result.RealtimeNextBucket = &next
		case domain.ResampleOriginBackfill:
			if result.Backfill == nil {
				return fmt.Errorf("active backfill state is missing")
			}
			result.Backfill.NextBucket = nextBucket.UTC()
			if !result.Backfill.NextBucket.Before(result.Backfill.End) {
				result.Backfill.State = domain.ResampleBackfillSyncing
			} else {
				result.Backfill.State = domain.ResampleBackfillRunning
			}
		}
		return nil
	}, domain.InstanceStatusSuccess)
}

func (r *TaskInstanceRepository) WaitResampleSource(ctx context.Context, spaceID, taskID string, expectedStateVersion int64, attempt int, nextRetryAt time.Time, lastError string) (bool, error) {
	return r.updateResampleTask(ctx, spaceID, taskID, expectedStateVersion, func(result *domain.ResampleTaskResult) error {
		if result.State != domain.ResampleTaskStateRunning || result.ActiveBucket == nil {
			return errResampleCASMismatch
		}
		if result.ActiveOrigin == domain.ResampleOriginRepair {
			// Repair is best-effort. Return transient failures to idle so a later
			// repair scan can retry without blocking realtime progress.
			result.State = domain.ResampleTaskStateIdle
			result.ActiveOrigin = ""
			result.ActiveBucket = nil
			result.LeaseUntil = nil
			result.Attempt = attempt
			result.NextRetryAt = nil
			result.LastError = strings.TrimSpace(lastError)
			return nil
		}
		result.State = domain.ResampleTaskStateWaitingSource
		result.LeaseUntil = nil
		result.Attempt = attempt
		retry := nextRetryAt.UTC()
		result.NextRetryAt = &retry
		result.LastError = strings.TrimSpace(lastError)
		if result.ActiveOrigin == domain.ResampleOriginBackfill && result.Backfill != nil {
			result.Backfill.State = domain.ResampleBackfillWaitingSource
		}
		return nil
	}, domain.InstanceStatusPending)
}

func (r *TaskInstanceRepository) FailResampleTask(ctx context.Context, spaceID, taskID string, expectedStateVersion int64, lastError string) (bool, error) {
	return r.updateResampleTask(ctx, spaceID, taskID, expectedStateVersion, func(result *domain.ResampleTaskResult) error {
		result.State = domain.ResampleTaskStateFailed
		result.LeaseUntil = nil
		result.NextRetryAt = nil
		result.LastError = strings.TrimSpace(lastError)
		if result.ActiveOrigin == domain.ResampleOriginBackfill && result.Backfill != nil {
			result.Backfill.State = domain.ResampleBackfillFailed
		}
		return nil
	}, domain.InstanceStatusFailed)
}

func (r *TaskInstanceRepository) RecoverExpiredResampleLeases(ctx context.Context, now time.Time, limit int) (int64, error) {
	return r.RecoverExpiredResampleLeasesInSpace(ctx, "", now, limit)
}

// RecoverExpiredResampleLeasesInSpace only recovers leases owned by one
// Collector space.
func (r *TaskInstanceRepository) RecoverExpiredResampleLeasesInSpace(ctx context.Context, spaceID string, now time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > maxPageSize {
		return 0, fmt.Errorf("recover limit must be between 1 and %d", maxPageSize)
	}
	now = now.UTC()
	var recovered int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instances []domain.TaskInstance
		query := tx.Where("t_collector_task_instances.c_data_type = ? AND t_collector_task_instances.c_is_deleted = ?", "kline_resample", false)
		if strings.TrimSpace(spaceID) != "" {
			query = query.Where("t_collector_task_instances.c_space_id = ?", strings.TrimSpace(spaceID))
		}
		if err := query.Order("t_collector_task_instances.c_id ASC").Find(&instances).Error; err != nil {
			return err
		}
		for _, instance := range instances {
			if recovered >= int64(limit) {
				break
			}
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err != nil || result.State != domain.ResampleTaskStateRunning || result.LeaseUntil == nil || result.LeaseUntil.After(now) {
				continue
			}
			version := result.StateVersion
			result.State = domain.ResampleTaskStateWaitingSource
			result.StateVersion++
			result.LeaseUntil = nil
			retry := now
			result.NextRetryAt = &retry
			result.LastError = "resample worker lease expired"
			encoded, err := result.Marshal()
			if err != nil {
				return err
			}
			updated, err := updateResampleResultCAS(tx, instance.SpaceID, instance.TaskID, instance.Result, version, encoded, domain.InstanceStatusPending)
			if err != nil {
				return err
			}
			if updated {
				recovered++
			}
		}
		return nil
	})
	return recovered, err
}

func (r *TaskInstanceRepository) StartResampleBackfill(ctx context.Context, spaceID, ruleID string, request domain.ResampleBackfillRequest) (int64, error) {
	if err := request.Validate(); err != nil {
		return 0, err
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Start = request.Start.UTC()
	request.End = request.End.UTC()
	var updated int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instances []domain.TaskInstance
		if err := tx.Where("c_space_id = ? AND c_rule_id = ? AND c_data_type = ? AND c_is_deleted = ?", strings.TrimSpace(spaceID), strings.TrimSpace(ruleID), "kline_resample", false).Order("c_id ASC").Find(&instances).Error; err != nil {
			return err
		}
		if len(instances) == 0 {
			return gorm.ErrRecordNotFound
		}
		parsed := make([]domain.ResampleTaskResult, len(instances))
		for index, instance := range instances {
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err != nil {
				return fmt.Errorf("task %s: %w", instance.TaskID, err)
			}
			if activeBackfill(result.Backfill) {
				if result.Backfill.RequestID != request.RequestID || !result.Backfill.Start.Equal(request.Start) || !result.Backfill.End.Equal(request.End) {
					return ErrResampleBackfillConflict
				}
			}
			parsed[index] = result
		}
		for index, instance := range instances {
			result := parsed[index]
			if activeBackfill(result.Backfill) {
				continue
			}
			version := result.StateVersion
			result.StateVersion++
			result.Backfill = &domain.ResampleBackfill{RequestID: request.RequestID, Start: request.Start, End: request.End, NextBucket: request.Start, State: domain.ResampleBackfillRunning}
			encoded, err := result.Marshal()
			if err != nil {
				return err
			}
			changed, err := updateResampleResultCAS(tx, instance.SpaceID, instance.TaskID, instance.Result, version, encoded, instance.LastExecStatus)
			if err != nil {
				return err
			}
			if !changed {
				return errResampleCASMismatch
			}
			updated++
		}
		return nil
	})
	return updated, err
}

func (r *TaskInstanceRepository) CancelResampleBackfill(ctx context.Context, spaceID, ruleID, requestID string) (int64, error) {
	return r.finishResampleBackfill(ctx, spaceID, ruleID, requestID, domain.ResampleBackfillCanceled)
}

func (r *TaskInstanceRepository) CompleteResampleBackfillSync(ctx context.Context, spaceID, ruleID, requestID string) (int64, error) {
	return r.finishResampleBackfill(ctx, spaceID, ruleID, requestID, domain.ResampleBackfillComplete)
}

func (r *TaskInstanceRepository) finishResampleBackfill(ctx context.Context, spaceID, ruleID, requestID string, state domain.ResampleBackfillState) (int64, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, fmt.Errorf("backfill request_id is required")
	}
	var updated int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instances []domain.TaskInstance
		if err := tx.Where("c_space_id = ? AND c_rule_id = ? AND c_data_type = ? AND c_is_deleted = ?", strings.TrimSpace(spaceID), strings.TrimSpace(ruleID), "kline_resample", false).Find(&instances).Error; err != nil {
			return err
		}
		for _, instance := range instances {
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err != nil {
				return err
			}
			if result.Backfill == nil || result.Backfill.RequestID != requestID || result.Backfill.State == state {
				continue
			}
			version := result.StateVersion
			result.StateVersion++
			result.Backfill.State = state
			if result.ActiveOrigin == domain.ResampleOriginBackfill {
				result.State = domain.ResampleTaskStateIdle
				result.ActiveOrigin = ""
				result.ActiveBucket = nil
				result.LeaseUntil = nil
				result.NextRetryAt = nil
			}
			encoded, err := result.Marshal()
			if err != nil {
				return err
			}
			changed, err := updateResampleResultCAS(tx, instance.SpaceID, instance.TaskID, instance.Result, version, encoded, instance.LastExecStatus)
			if err != nil {
				return err
			}
			if changed {
				updated++
			}
		}
		return nil
	})
	return updated, err
}

var errResampleCASMismatch = errors.New("resample task state changed")

func (r *TaskInstanceRepository) updateResampleTask(ctx context.Context, spaceID, taskID string, expectedStateVersion int64, mutate func(*domain.ResampleTaskResult) error, status int) (bool, error) {
	updated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		instance, result, raw, err := loadResampleTask(tx, spaceID, taskID)
		if err != nil {
			return err
		}
		if result.StateVersion != expectedStateVersion {
			return nil
		}
		if err := mutate(&result); err != nil {
			if errors.Is(err, errResampleCASMismatch) {
				return nil
			}
			return err
		}
		result.StateVersion++
		encoded, err := result.Marshal()
		if err != nil {
			return err
		}
		updated, err = updateResampleResultCAS(tx, instance.SpaceID, instance.TaskID, raw, expectedStateVersion, encoded, status)
		return err
	})
	return updated, err
}

func loadResampleTask(tx *gorm.DB, spaceID, taskID string) (domain.TaskInstance, domain.ResampleTaskResult, string, error) {
	var instance domain.TaskInstance
	err := tx.Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ? AND c_is_deleted = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID), "kline_resample", false).First(&instance).Error
	if err != nil {
		return instance, domain.ResampleTaskResult{}, "", err
	}
	result, err := domain.ParseResampleTaskResult(instance.Result)
	return instance, result, instance.Result, err
}

func updateResampleResultCAS(tx *gorm.DB, spaceID, taskID, previous string, expectedVersion int64, encoded string, status int) (bool, error) {
	result := tx.Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ? AND c_result = ?", spaceID, taskID, "kline_resample", previous).
		Where("json_valid(c_result) AND CAST(json_extract(c_result, '$.state_version') AS INTEGER) = ?", expectedVersion).
		Updates(map[string]any{"c_result": encoded, "c_last_exec_status": status, "c_mtime": time.Now().UTC()})
	return result.RowsAffected == 1, result.Error
}

func markCorruptResampleResult(tx *gorm.DB, instance domain.TaskInstance, cause error) error {
	failed := domain.NewResampleTaskResult(time.Time{})
	failed.State = domain.ResampleTaskStateFailed
	failed.StateVersion = 1
	failed.LastError = "invalid persisted resample state: " + cause.Error()
	encoded, err := failed.Marshal()
	if err != nil {
		return err
	}
	return tx.Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_task_id = ? AND c_result = ?", instance.SpaceID, instance.TaskID, instance.Result).
		Updates(map[string]any{"c_result": encoded, "c_last_exec_status": domain.InstanceStatusFailed, "c_mtime": time.Now().UTC()}).Error
}

func activeBackfill(backfill *domain.ResampleBackfill) bool {
	if backfill == nil {
		return false
	}
	return backfill.State == domain.ResampleBackfillRunning || backfill.State == domain.ResampleBackfillWaitingSource || backfill.State == domain.ResampleBackfillSyncing
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
