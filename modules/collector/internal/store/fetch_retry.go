package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxTerminalRetryCleanupRows  = 10000
	retryCleanupBatchIdentityRef = `(
		(retries.c_write_target_id <> '' AND batches.c_write_target_id = retries.c_write_target_id)
		OR (retries.c_instance_id <> '' AND (
		  batches.c_instance_id = retries.c_instance_id
		  OR EXISTS (
		    SELECT 1 FROM t_collector_fetch_batch_items AS batch_items
		    WHERE batch_items.c_space_id = batches.c_space_id
		      AND batch_items.c_batch_id = batches.c_batch_id
		      AND batch_items.c_instance_id = retries.c_instance_id
		  )
		))
	)`
	retryCleanupBatchRequestItemRef = `EXISTS (
		SELECT 1 FROM json_each(
		  CASE WHEN json_valid(batches.c_request_json)
			  THEN COALESCE(json_extract(batches.c_request_json, '$.items'), '[]')
			  ELSE '[]' END
		) AS request_items
		WHERE CASE WHEN json_valid(request_items.value)
		           THEN json_extract(request_items.value, '$.source_event_id')
		           ELSE NULL END = retries.c_retry_key
	)`
	retryCleanupTimedOutSyncPointRef = `retries.c_source_batch_id = COALESCE(
		NULLIF(json_extract(
		  CASE WHEN json_valid(batches.c_request_json) THEN batches.c_request_json ELSE '{}' END,
		  '$.sync_point_id'
		), ''), batches.c_batch_id
	)`
	retryCleanupActiveBatchGuard = `AND NOT EXISTS (
		SELECT 1 FROM t_collector_fetch_batches AS batches
		WHERE batches.c_space_id = retries.c_space_id
		  AND (
		    (batches.c_status IN ('planned', 'dispatched') AND (
		      ` + retryCleanupBatchIdentityRef + ` OR ` + retryCleanupBatchRequestItemRef + `
		    ))
		    OR (batches.c_status = 'timed_out' AND batches.c_late_completion = 0 AND (
		      ` + retryCleanupBatchIdentityRef + ` OR ` + retryCleanupBatchRequestItemRef + `
		      OR ` + retryCleanupTimedOutSyncPointRef + `
		    ))
		  )
	)`
	retryCleanupSucceededPageSQL = `SELECT retries.c_id
		FROM t_collector_fetch_retry_items AS retries INDEXED BY idx_collector_fetch_retry_cleanup_succeeded
		WHERE retries.c_mtime < ? AND retries.c_status IN ('succeeded', 'superseded') ` + retryCleanupActiveBatchGuard + `
		ORDER BY retries.c_mtime, retries.c_id
		LIMIT ?`
	retryCleanupPermanentPageSQL = `SELECT retries.c_id
		FROM t_collector_fetch_retry_items AS retries INDEXED BY idx_collector_fetch_retry_cleanup_permanent
		WHERE retries.c_mtime < ? AND retries.c_status = 'permanent_failed' AND retries.c_period_failure_report_state <> 'pending' ` + retryCleanupActiveBatchGuard + `
		ORDER BY retries.c_mtime, retries.c_id
		LIMIT ?`
	retryCleanupSucceededPageSpaceSQL = `SELECT retries.c_id
		FROM t_collector_fetch_retry_items AS retries INDEXED BY idx_collector_fetch_retry_cleanup_succeeded_space
		WHERE retries.c_space_id = ? AND retries.c_mtime < ? AND retries.c_status IN ('succeeded', 'superseded') ` + retryCleanupActiveBatchGuard + `
		ORDER BY retries.c_mtime, retries.c_id
		LIMIT ?`
	retryCleanupPermanentPageSpaceSQL = `SELECT retries.c_id
		FROM t_collector_fetch_retry_items AS retries INDEXED BY idx_collector_fetch_retry_cleanup_permanent_space
		WHERE retries.c_space_id = ? AND retries.c_mtime < ? AND retries.c_status = 'permanent_failed' AND retries.c_period_failure_report_state <> 'pending' ` + retryCleanupActiveBatchGuard + `
		ORDER BY retries.c_mtime, retries.c_id
		LIMIT ?`
)

type FetchRetryRepository struct{ db *gorm.DB }

func NewFetchRetryRepository(db *gorm.DB) *FetchRetryRepository { return &FetchRetryRepository{db: db} }

func (r *FetchRetryRepository) DeleteByTaskID(ctx context.Context, spaceID, taskID string) error {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	// Only target-scoped retries are owned by one CollectionTask. Fetch retries
	// belong to the shared instance and survive while another WriteTarget exists.
	// Pending permanent-failure receipts are durable outbox work, not task-owned
	// runtime state, and must survive target deletion until Storage settles them.
	return r.db.WithContext(ctx).Where(`c_space_id = ? AND c_write_target_id IN (SELECT c_write_target_id FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_task_id = ?) AND NOT (c_status = 'permanent_failed' AND c_period_failure_report_state = ?)`, spaceID, spaceID, taskID, domain.PeriodFailureReportPending).Delete(&domain.RetryItem{}).Error
}

func (r *FetchRetryRepository) Upsert(ctx context.Context, item *domain.RetryItem) error {
	if item == nil {
		return gorm.ErrInvalidData
	}
	now := time.Now().UTC()
	if item.CreateTime.IsZero() {
		item.CreateTime = now
	}
	if item.PeriodFailureReportState == "" {
		item.PeriodFailureReportState = domain.PeriodFailureReportPending
	}
	if strings.TrimSpace(item.PeriodFailureResultsJSON) == "" {
		item.PeriodFailureResultsJSON = "[]"
	}
	item.ModifyTime = now
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_retry_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_source_batch_id":      clause.Expr{SQL: "CASE WHEN c_source_batch_id <> '' THEN c_source_batch_id ELSE excluded.c_source_batch_id END"},
			"c_batch_kind":           clause.Expr{SQL: "excluded.c_batch_kind"},
			"c_period_time":          clause.Expr{SQL: "CASE WHEN c_period_time IS NOT NULL THEN c_period_time ELSE excluded.c_period_time END"},
			"c_period_deadline_at":   clause.Expr{SQL: "CASE WHEN c_period_deadline_at IS NOT NULL THEN c_period_deadline_at ELSE excluded.c_period_deadline_at END"},
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

func (r *FetchRetryRepository) ListDuePrioritized(ctx context.Context, spaceID string, now time.Time, recentLimit, historicalLimit int, excludedRetryKeys ...string) (recent, historical []domain.RetryItem, err error) {
	if recentLimit < 0 || historicalLimit < 0 {
		return nil, nil, gorm.ErrInvalidData
	}
	now = now.UTC()
	base := r.db.WithContext(ctx).Where("c_space_id = ? AND c_status = ? AND c_next_retry_at IS NOT NULL AND c_next_retry_at <= ?", spaceID, "pending", now)
	excludedRetryKeys = uniqueNonEmptyStrings(excludedRetryKeys)
	if len(excludedRetryKeys) > 0 {
		base = base.Where("c_retry_key NOT IN ?", excludedRetryKeys)
	}
	if recentLimit > 0 {
		err = base.Session(&gorm.Session{}).
			Where("c_period_deadline_at IS NOT NULL AND c_period_deadline_at > ?", now).
			Order("c_period_deadline_at ASC, c_next_retry_at ASC").Limit(recentLimit).Find(&recent).Error
		if err != nil {
			return nil, nil, err
		}
	}
	if historicalLimit > 0 {
		err = base.Session(&gorm.Session{}).
			Where("c_period_deadline_at IS NULL OR c_period_deadline_at <= ?", now).
			Order("c_next_retry_at ASC").Limit(historicalLimit).Find(&historical).Error
		if err != nil {
			return nil, nil, err
		}
	}
	return recent, historical, nil
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
	updates := map[string]any{"c_status": status, "c_mtime": time.Now().UTC()}
	if status == "succeeded" {
		updates["c_period_failure_report_state"] = domain.PeriodFailureReportPending
		updates["c_period_failure_results_json"] = "[]"
		updates["c_period_failure_last_error"] = ""
		updates["c_period_failure_deadline_exceeded_at"] = nil
	}
	return r.db.WithContext(ctx).Model(&domain.RetryItem{}).
		Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).
		Updates(updates).Error
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
		reportState := domain.PeriodFailureReportPending
		reportError := ""
		if !retryHasReportableFailureSnapshot(&item) {
			reportState = domain.PeriodFailureReportMissedDeadline
			reportError = "durable period failure snapshot unavailable"
		}
		result := tx.Model(&domain.RetryItem{}).
			Where("c_space_id = ? AND c_retry_key = ? AND c_status IN ?", spaceID, retryKey, []string{"pending", "dispatched"}).
			Updates(map[string]any{
				"c_status": "permanent_failed", "c_next_retry_at": nil,
				"c_last_error_type": errorType, "c_last_error_summary": errorSummary,
				"c_period_failure_report_state": reportState, "c_period_failure_last_error": reportError,
				"c_mtime": now,
			})
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

func retryHasReportableFailureSnapshot(item *domain.RetryItem) bool {
	if item == nil {
		return false
	}
	var targets []domain.WriteTarget
	if err := json.Unmarshal([]byte(item.FailureTargetsJSON), &targets); err != nil || len(targets) == 0 {
		return false
	}
	targetIDs := make(map[string]struct{}, len(targets))
	type datasetSeriesIdentity struct {
		seriesHash    string
		expectedCount uint32
	}
	datasetSeries := make(map[string]datasetSeriesIdentity, len(targets))
	for _, target := range targets {
		targetID := strings.TrimSpace(target.ID)
		datasetID := strings.TrimSpace(target.DatasetID)
		seriesHash := strings.TrimSpace(target.SeriesHash)
		if targetID == "" || targetID != target.ID || datasetID == "" || datasetID != target.DatasetID || seriesHash == "" || seriesHash != target.SeriesHash || target.ExpectedCount == 0 || target.SeriesIndex >= target.ExpectedCount || (target.SpaceID != "" && target.SpaceID != item.SpaceID) {
			return false
		}
		if _, exists := targetIDs[targetID]; exists {
			return false
		}
		targetIDs[targetID] = struct{}{}
		identity := datasetSeriesIdentity{seriesHash: seriesHash, expectedCount: target.ExpectedCount}
		if previous, exists := datasetSeries[datasetID]; exists && previous != identity {
			return false
		}
		datasetSeries[datasetID] = identity
	}
	var collectionItem domain.CollectionItem
	if err := json.Unmarshal([]byte(item.TaskJSON), &collectionItem); err != nil || strings.TrimSpace(collectionItem.MarketType) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(collectionItem.MarketType)) {
	case "spot", "swap", "equity", "stockcn":
	default:
		return false
	}
	frequency := strings.TrimSpace(item.Frequency)
	if frequency == "" || frequency != item.Frequency || item.TargetDataTime.IsZero() {
		return false
	}
	_, err := marketdata.ParseFrequency(frequency)
	return err == nil
}

func (r *FetchRetryRepository) ListPendingPeriodFailuresAfter(ctx context.Context, spaceID, afterRetryKey string, limit int) ([]domain.RetryItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []domain.RetryItem
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status = ? AND c_period_failure_report_state = ? AND c_failure_targets_json <> '' AND c_failure_targets_json <> '[]' AND c_retry_key > ?", strings.TrimSpace(spaceID), "permanent_failed", domain.PeriodFailureReportPending, strings.TrimSpace(afterRetryKey)).
		Order("c_retry_key ASC").Limit(limit).Find(&items).Error
	return items, err
}

func (r *FetchRetryRepository) CountPendingPeriodFailuresByFrequency(ctx context.Context, spaceID string) (map[string]int64, error) {
	var rows []struct {
		Frequency string `gorm:"column:c_frequency"`
		Count     int64  `gorm:"column:count"`
	}
	err := r.db.WithContext(ctx).Model(&domain.RetryItem{}).
		Select("c_frequency, COUNT(*) AS count").
		Where("c_space_id = ? AND c_status = ? AND c_period_failure_report_state = ? AND c_failure_targets_json <> '' AND c_failure_targets_json <> '[]'", strings.TrimSpace(spaceID), "permanent_failed", domain.PeriodFailureReportPending).
		Group("c_frequency").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.Frequency] = row.Count
	}
	return counts, nil
}

func (r *FetchRetryRepository) ApplyPeriodFailureReportResults(ctx context.Context, spaceID, retryKey string, results []domain.PeriodFailureTargetResult, lastError string) error {
	spaceID, retryKey = strings.TrimSpace(spaceID), strings.TrimSpace(retryKey)
	if spaceID == "" || retryKey == "" {
		return gorm.ErrInvalidData
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item domain.RetryItem
		if err := tx.Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).First(&item).Error; err != nil {
			return err
		}
		var targets []domain.WriteTarget
		if err := json.Unmarshal([]byte(item.FailureTargetsJSON), &targets); err != nil {
			return fmt.Errorf("decode durable failure targets: %w", err)
		}
		if len(targets) == 0 {
			return fmt.Errorf("retry item has no durable failure targets")
		}
		normalizedFrequency, err := marketdata.ParseFrequency(item.Frequency)
		if err != nil {
			return fmt.Errorf("retry item has invalid failure frequency %q: %w", item.Frequency, err)
		}
		storageFrequency, err := frequencypkg.Normalize(item.Frequency)
		if err != nil {
			return fmt.Errorf("retry item has invalid Storage failure frequency %q: %w", item.Frequency, err)
		}
		targetByID := make(map[string]domain.WriteTarget, len(targets))
		for _, target := range targets {
			id := strings.TrimSpace(target.ID)
			if id == "" || (target.SpaceID != "" && target.SpaceID != spaceID) {
				return fmt.Errorf("retry item has invalid durable failure target")
			}
			if _, exists := targetByID[id]; exists {
				return fmt.Errorf("retry item has duplicate durable failure target %s", id)
			}
			target.ID = id
			targetByID[id] = target
		}
		var persisted []domain.PeriodFailureTargetResult
		if strings.TrimSpace(item.PeriodFailureResultsJSON) != "" {
			if err := json.Unmarshal([]byte(item.PeriodFailureResultsJSON), &persisted); err != nil {
				return fmt.Errorf("decode persisted period failure results: %w", err)
			}
		}
		merged := make(map[string]domain.PeriodFailureTargetResult, len(persisted)+len(results))
		for _, result := range persisted {
			target, exists := targetByID[result.WriteTargetID]
			if !exists || !periodFailureResultMatchesTarget(spaceID, normalizedFrequency, item.TargetDataTime, target, result) {
				return fmt.Errorf("persisted period failure result identity does not match durable write_target_id %s", result.WriteTargetID)
			}
			if authoritativePeriodFailureDisposition(result.Disposition) {
				merged[result.WriteTargetID] = result
			}
		}
		seen := make(map[string]struct{}, len(results))
		for _, result := range results {
			target, exists := targetByID[strings.TrimSpace(result.WriteTargetID)]
			if !exists {
				return fmt.Errorf("period failure result targets unknown write_target_id %q", result.WriteTargetID)
			}
			if _, duplicate := seen[target.ID]; duplicate {
				return fmt.Errorf("duplicate period failure result for write_target_id %s", target.ID)
			}
			seen[target.ID] = struct{}{}
			if !periodFailureResultMatchesTarget(spaceID, normalizedFrequency, item.TargetDataTime, target, result) {
				return fmt.Errorf("period failure result identity does not match durable write_target_id %s", target.ID)
			}
			result.WriteTargetID = target.ID
			result.PeriodTime = result.PeriodTime.UTC()
			result.ObservedAt = result.ObservedAt.UTC()
			if previous, exists := merged[target.ID]; exists && authoritativePeriodFailureDisposition(previous.Disposition) {
				continue
			}
			merged[target.ID] = result
		}
		ordered := make([]domain.PeriodFailureTargetResult, 0, len(merged))
		for _, result := range merged {
			ordered = append(ordered, result)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].WriteTargetID < ordered[j].WriteTargetID })
		resultsJSON, err := json.Marshal(ordered)
		if err != nil {
			return err
		}
		state := domain.AggregatePeriodFailureReportState(targets, ordered)
		updates := map[string]any{
			"c_period_failure_report_state": string(state),
			"c_period_failure_results_json": string(resultsJSON),
			"c_mtime":                       time.Now().UTC(),
		}
		if strings.TrimSpace(lastError) != "" {
			updates["c_period_failure_last_error"] = strings.TrimSpace(lastError)
		} else if len(results) > 0 {
			updates["c_period_failure_last_error"] = ""
		}
		if item.PeriodFailureDeadlineExceededAt == nil {
			for _, target := range targets {
				var periodState struct {
					DeadlineAt time.Time `gorm:"column:c_deadline_at"`
				}
				result := tx.Table("t_collector_period_storage_states").
					Select("c_deadline_at").
					Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", spaceID, target.DatasetID, storageFrequency, item.TargetDataTime.UTC()).
					Limit(1).Find(&periodState)
				if result.Error == nil && result.RowsAffected > 0 && !periodState.DeadlineAt.IsZero() && !time.Now().UTC().Before(periodState.DeadlineAt.UTC()) {
					updates["c_period_failure_deadline_exceeded_at"] = time.Now().UTC()
					break
				}
			}
		}
		return tx.Model(&domain.RetryItem{}).Where("c_space_id = ? AND c_retry_key = ?", spaceID, retryKey).Updates(updates).Error
	})
}

func periodFailureResultMatchesTarget(spaceID string, frequency marketdata.Frequency, targetPeriod time.Time, target domain.WriteTarget, result domain.PeriodFailureTargetResult) bool {
	resultFrequency, resultFrequencyErr := marketdata.ParseFrequency(result.Frequency)
	return result.SpaceID == spaceID && result.DatasetID == target.DatasetID && resultFrequencyErr == nil && resultFrequency == frequency && result.PeriodTime.UTC().Equal(targetPeriod.UTC()) && result.SeriesIndex == target.SeriesIndex && result.SeriesHash == target.SeriesHash && result.ExpectedCount == target.ExpectedCount
}

func authoritativePeriodFailureDisposition(disposition string) bool {
	switch disposition {
	case "recorded", "already_succeeded", "missed_deadline":
		return true
	default:
		return false
	}
}

func (r *FetchRetryRepository) Cleanup(ctx context.Context, before time.Time) error {
	return r.cleanup(ctx, "", before, maxTerminalRetryCleanupRows)
}

// CleanupSpace applies a bounded retry-history page to one Space.
func (r *FetchRetryRepository) CleanupSpace(ctx context.Context, spaceID string, before time.Time, budget int) error {
	_, err := r.CleanupSpaceWithCount(ctx, spaceID, before, budget)
	return err
}

// CleanupSpaceWithCount returns only committed physical deletes.
func (r *FetchRetryRepository) CleanupSpaceWithCount(ctx context.Context, spaceID string, before time.Time, budget int) (int64, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required for scoped retry cleanup")
	}
	return r.cleanupWithCount(ctx, spaceID, before, budget)
}

func (r *FetchRetryRepository) cleanup(ctx context.Context, spaceID string, before time.Time, budget int) error {
	_, err := r.cleanupWithCount(ctx, spaceID, before, budget)
	return err
}

func (r *FetchRetryRepository) cleanupWithCount(ctx context.Context, spaceID string, before time.Time, budget int) (int64, error) {
	budget = min(max(0, budget), maxTerminalRetryCleanupRows)
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		remaining := budget
		queries := []string{retryCleanupSucceededPageSQL, retryCleanupPermanentPageSQL}
		if spaceID != "" {
			queries = []string{retryCleanupSucceededPageSpaceSQL, retryCleanupPermanentPageSpaceSQL}
		}
		for pass := 0; pass < 2 && remaining > 0; pass++ {
			for index, query := range queries {
				limit := remaining
				if pass == 0 {
					remainingGroups := len(queries) - index
					limit = (remaining + remainingGroups - 1) / remainingGroups
				}
				args := []any{before.UTC(), limit}
				if spaceID != "" {
					args = []any{spaceID, before.UTC(), limit}
				}
				result := tx.Exec(`DELETE FROM t_collector_fetch_retry_items WHERE c_id IN (`+query+`)`, args...)
				if result.Error != nil {
					return result.Error
				}
				remaining -= int(result.RowsAffected)
				deleted += result.RowsAffected
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
