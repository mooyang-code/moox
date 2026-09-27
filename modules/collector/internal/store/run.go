package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/rs/xid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RunRepository struct{ db *gorm.DB }

type runAggregate struct {
	Instances       int64 `gorm:"column:instances"`
	InstanceSuccess int64 `gorm:"column:instance_success"`
	InstanceFailed  int64 `gorm:"column:instance_failed"`
	InstancePending int64 `gorm:"column:instance_pending"`
	Batches         int64 `gorm:"column:batches"`
	BatchActive     int64 `gorm:"column:batch_active"`
	TargetTotal     int64 `gorm:"column:target_total"`
	TargetSuccess   int64 `gorm:"column:target_success"`
	TargetFailed    int64 `gorm:"column:target_failed"`
	TargetPending   int64 `gorm:"column:target_pending"`
	RetryActive     int64 `gorm:"column:retry_active"`
}

func NewRunRepository(db *gorm.DB) *RunRepository { return &RunRepository{db: db} }

// Create creates a new execution round unconditionally. It is the entry point
// for manual replay/backfill/repair semantics where the same request_key must
// be executable again in a distinct Run.
func (r *RunRepository) Create(ctx context.Context, spaceID, runType, frequency string, targetTime *time.Time) (*domain.CollectionRun, error) {
	spaceID, runType = strings.TrimSpace(spaceID), strings.TrimSpace(runType)
	if spaceID == "" || runType == "" {
		return nil, fmt.Errorf("space_id and run_type are required")
	}
	runID := xid.New().String()
	item := &domain.CollectionRun{
		SpaceID: spaceID, RunID: runID, RunKey: runType + ":" + runID,
		RunType: runType, Frequency: strings.TrimSpace(frequency), TargetTime: targetTime,
		Status: domain.RunStatusPlanned, CreateTime: time.Now().UTC(), ModifyTime: time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *RunRepository) GetOrCreateScheduled(ctx context.Context, spaceID, runKey, runType, frequency string, targetTime time.Time) (*domain.CollectionRun, error) {
	item := &domain.CollectionRun{SpaceID: strings.TrimSpace(spaceID), RunID: xid.New().String(), RunKey: strings.TrimSpace(runKey), RunType: strings.TrimSpace(runType), Frequency: strings.TrimSpace(frequency), TargetTime: &targetTime, Status: domain.RunStatusPlanned, CreateTime: time.Now().UTC(), ModifyTime: time.Now().UTC()}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_run_key"}}, DoNothing: true}).Create(item).Error
	if err != nil {
		return nil, err
	}
	var out domain.CollectionRun
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_run_key = ?", item.SpaceID, item.RunKey).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *RunRepository) Get(ctx context.Context, spaceID, runID string) (*domain.CollectionRun, error) {
	var out domain.CollectionRun
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_run_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(runID)).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *RunRepository) UpdateStatus(ctx context.Context, spaceID, runID string, status domain.RunStatus, summary string) error {
	return r.db.WithContext(ctx).Model(&domain.CollectionRun{}).Where("c_space_id = ? AND c_run_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(runID)).Updates(map[string]any{"c_status": status, "c_error_summary": summary, "c_mtime": time.Now().UTC()}).Error
}

// ReconcileOpenRuns converges planned/active runs from durable instance,
// target, batch and retry state. Scheduler calls it once per tick, so terminal
// state never depends on an SCF process staying alive after it writes data.
func (r *RunRepository) ReconcileOpenRuns(ctx context.Context, spaceID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("run repository is not initialized")
	}
	var runs []domain.CollectionRun
	if err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_status IN ?", strings.TrimSpace(spaceID), []domain.RunStatus{domain.RunStatusPlanned, domain.RunStatusActive}).
		Order("c_id ASC").Limit(500).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		if _, err := r.Reconcile(ctx, run.SpaceID, run.RunID); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile derives one Run status. Runs with SCF batches require target-level
// fan-out completion; Timer-owned runs have no batches and therefore complete
// from instance freshness alone.
func (r *RunRepository) Reconcile(ctx context.Context, spaceID, runID string) (domain.RunStatus, error) {
	spaceID, runID = strings.TrimSpace(spaceID), strings.TrimSpace(runID)
	if spaceID == "" || runID == "" {
		return "", fmt.Errorf("space_id and run_id are required")
	}
	agg, err := r.aggregate(ctx, spaceID, runID)
	if err != nil {
		return "", err
	}
	status := domain.RunStatusPlanned
	switch {
	case agg.Instances == 0:
		status = domain.RunStatusPlanned
	case agg.BatchActive > 0 || agg.RetryActive > 0 || agg.InstancePending > 0:
		status = domain.RunStatusActive
	case agg.InstanceFailed == agg.Instances:
		status = domain.RunStatusFailed
	case agg.InstanceFailed > 0:
		status = domain.RunStatusPartialFailed
	case agg.Batches == 0:
		status = domain.RunStatusSucceeded
	case agg.TargetPending > 0:
		status = domain.RunStatusActive
	case agg.TargetFailed > 0 && agg.TargetSuccess > 0:
		status = domain.RunStatusPartialFailed
	case agg.TargetFailed > 0:
		status = domain.RunStatusFailed
	default:
		status = domain.RunStatusSucceeded
	}
	summary := fmt.Sprintf("instances=%d success=%d failed=%d pending=%d targets=%d target_success=%d target_failed=%d target_pending=%d batches=%d batch_active=%d retry_active=%d", agg.Instances, agg.InstanceSuccess, agg.InstanceFailed, agg.InstancePending, agg.TargetTotal, agg.TargetSuccess, agg.TargetFailed, agg.TargetPending, agg.Batches, agg.BatchActive, agg.RetryActive)
	if err := r.UpdateStatus(ctx, spaceID, runID, status, summary); err != nil {
		return "", err
	}
	return status, nil
}

func (r *RunRepository) aggregate(ctx context.Context, spaceID, runID string) (runAggregate, error) {
	var out runAggregate
	var instances struct {
		Total   int64 `gorm:"column:instances"`
		Success int64 `gorm:"column:instance_success"`
		Failed  int64 `gorm:"column:instance_failed"`
		Pending int64 `gorm:"column:instance_pending"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT
		COUNT(*) AS instances,
		COALESCE(SUM(CASE WHEN c_last_exec_status = ? THEN 1 ELSE 0 END), 0) AS instance_success,
		COALESCE(SUM(CASE WHEN c_last_exec_status = ? THEN 1 ELSE 0 END), 0) AS instance_failed,
		COALESCE(SUM(CASE WHEN c_last_exec_status NOT IN (?, ?) THEN 1 ELSE 0 END), 0) AS instance_pending
		FROM t_collector_task_instances WHERE c_space_id = ? AND c_run_id = ?`, domain.InstanceStatusSuccess, domain.InstanceStatusFailed, domain.InstanceStatusSuccess, domain.InstanceStatusFailed, spaceID, runID).Scan(&instances).Error; err != nil {
		return out, err
	}
	out.Instances, out.InstanceSuccess, out.InstanceFailed, out.InstancePending = instances.Total, instances.Success, instances.Failed, instances.Pending

	var targets struct {
		Total   int64 `gorm:"column:target_total"`
		Success int64 `gorm:"column:target_success"`
		Failed  int64 `gorm:"column:target_failed"`
		Pending int64 `gorm:"column:target_pending"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT
		COUNT(*) AS target_total,
		COALESCE(SUM(CASE WHEN LOWER(targets.c_status) IN ('succeeded','success','completed') THEN 1 ELSE 0 END), 0) AS target_success,
		COALESCE(SUM(CASE WHEN LOWER(targets.c_status) IN ('failed','error','permanent_failed') THEN 1 ELSE 0 END), 0) AS target_failed,
		COALESCE(SUM(CASE WHEN LOWER(targets.c_status) NOT IN ('succeeded','success','completed','failed','error','permanent_failed') THEN 1 ELSE 0 END), 0) AS target_pending
		FROM t_collector_instance_write_targets targets
		JOIN t_collector_task_instances instances ON instances.c_space_id = targets.c_space_id AND instances.c_instance_id = targets.c_instance_id
		WHERE instances.c_space_id = ? AND instances.c_run_id = ?`, spaceID, runID).Scan(&targets).Error; err != nil {
		return out, err
	}
	out.TargetTotal, out.TargetSuccess, out.TargetFailed, out.TargetPending = targets.Total, targets.Success, targets.Failed, targets.Pending

	var batches struct {
		Total  int64 `gorm:"column:batches"`
		Active int64 `gorm:"column:batch_active"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT
		COUNT(*) AS batches,
		COALESCE(SUM(CASE WHEN batches.c_status NOT IN ('succeeded','partial_failed','failed','timed_out') THEN 1 ELSE 0 END), 0) AS batch_active
		FROM t_collector_fetch_batches batches
		WHERE batches.c_space_id = ? AND EXISTS (
			SELECT 1 FROM t_collector_fetch_batch_items items
			JOIN t_collector_task_instances instances ON instances.c_space_id = items.c_space_id AND instances.c_instance_id = items.c_instance_id
			WHERE items.c_space_id = batches.c_space_id AND items.c_batch_id = batches.c_batch_id AND instances.c_run_id = ?
		)`, spaceID, runID).Scan(&batches).Error; err != nil {
		return out, err
	}
	out.Batches, out.BatchActive = batches.Total, batches.Active

	var retries struct {
		Active int64 `gorm:"column:retry_active"`
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT COUNT(*) AS retry_active
		FROM t_collector_fetch_retry_items retries
		JOIN t_collector_task_instances instances ON instances.c_space_id = retries.c_space_id AND instances.c_instance_id = retries.c_instance_id
		WHERE retries.c_space_id = ? AND instances.c_run_id = ? AND retries.c_status IN ('pending','dispatched')`, spaceID, runID).Scan(&retries).Error; err != nil {
		return out, err
	}
	out.RetryActive = retries.Active
	return out, nil
}
