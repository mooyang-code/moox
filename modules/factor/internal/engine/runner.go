package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

type observedStore struct {
	storageio.Store
	health *Health
}

func (s *observedStore) WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []storageio.ResultRow) error {
	err := s.Store.WriteRows(ctx, spaceID, datasetID, commitID, rows)
	s.health.RecordStorageWrite(err == nil)
	return err
}

type measuredRunner struct {
	inner interface {
		Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
	}
	metrics *observability.Metrics
	health  *Health
	runs    *runTracker

	// carried 是每个成员连续沿用参数的期数（任务/别名 → 期数），实时周期按冻结来源更新。
	carriedMu sync.Mutex
	carried   map[string]int
}

func (r *measuredRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	started := time.Now()
	// 内存预留的优先级：实时周期最高，补算、恢复与失效重算让路。
	prio := memadmit.PriorityBackground
	if plan.Mode == pipeline.ModeLive {
		prio = memadmit.PriorityRealtime
	}
	ctx = memadmit.WithPriority(ctx, prio)
	periodTime := plan.PeriodTime
	if periodTime.IsZero() {
		periodTime = plan.TargetStart
	}
	r.health.StartRunner(plan.Set.TaskID, started)
	defer r.health.EndRunner(plan.Set.TaskID)
	log.InfoContextf(ctx, "factor_period_start task_id=%s period_time=%s mode=%d", plan.Set.TaskID, periodTime.UTC().Format(time.RFC3339), plan.Mode)
	outcome, err := r.inner.Run(ctx, plan)
	taskID := plan.Set.TaskID
	for stage, duration := range outcome.StageDurations {
		r.metrics.PeriodDuration.WithLabelValues(taskID, stage).Observe(duration.Seconds())
	}
	status := outcome.Status
	if err != nil {
		status = "failed"
	}
	if status == "" {
		status = "complete"
	}
	if plan.Mode == pipeline.ModeLive {
		r.metrics.PeriodTotal.WithLabelValues(taskID, status).Inc()
		r.metrics.PeriodLag.WithLabelValues(taskID).Set(max(0, time.Since(periodTime).Seconds()))
		if !periodTime.IsZero() && (err == nil || outcome.Status != "") {
			r.metrics.LastPeriodTime.WithLabelValues(taskID).Set(float64(periodTime.Unix()))
			r.runs.record(taskID, periodTime, status, outcome, time.Now())
		}
		r.recordParams(taskID, outcome)
	}
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			reason := factor.Status
			if reason == "" {
				reason = "failed"
			}
			r.metrics.Failures.WithLabelValues(taskID, factor.Member, reason).Inc()
			log.ErrorContextf(ctx, "factor_compute_failed task_id=%s member=%s factor_id=%s reason=%s", taskID, factor.Member, factor.FactorID, reason)
		}
	}
	if err != nil {
		r.metrics.Failures.WithLabelValues(taskID, periodFailureFactor, periodFailureReason(err)).Inc()
		log.ErrorContextf(ctx, "factor_period_failed task_id=%s period_time=%s error=%v", taskID, periodTime.UTC().Format(time.RFC3339), err)
	} else {
		log.InfoContextf(ctx, "factor_period_done task_id=%s period_time=%s status=%s rows=%d warming=%d duration=%s %s", taskID, periodTime.UTC().Format(time.RFC3339), status, outcome.RowsWritten, outcome.Warming, time.Since(started), stageSummary(outcome.StageDurations))
	}
	return outcome, err
}

// recordParams 按每个成员这一期冻结参数的来源计数，并维护连续沿用的期数：沿用（含离线沿用）加一，
// 新提交生效时归零；默认值（从未提交过）不算沿用。
func (r *measuredRunner) recordParams(taskID string, outcome pipeline.Outcome) {
	r.carriedMu.Lock()
	defer r.carriedMu.Unlock()
	if r.carried == nil {
		r.carried = map[string]int{}
	}
	for _, state := range outcome.Factors {
		if state.ParamsSource == "" {
			continue
		}
		r.metrics.ParamFreezes.WithLabelValues(taskID, state.ParamsSource).Inc()
		key := taskID + "/" + state.Member
		switch state.ParamsSource {
		case domain.ParamsCarried, domain.ParamsCarriedOffline:
			r.carried[key]++
		default:
			r.carried[key] = 0
		}
		r.metrics.ParamCarriedPeriods.WithLabelValues(taskID, state.Member).Set(float64(r.carried[key]))
	}
}

// carriedPeriods 返回成员当前连续沿用参数的期数（没有记录为 0）。
func (r *measuredRunner) carriedPeriods(taskID, member string) int {
	r.carriedMu.Lock()
	defer r.carriedMu.Unlock()
	return r.carried[taskID+"/"+member]
}

// pipelineStages is the order a period runs through; the log line lists the
// per-stage time so slow reads, compute and writes can be told apart.
var pipelineStages = []string{"plan", "load", "compute", "assemble", "write", "report"}

func stageSummary(durations map[string]time.Duration) string {
	parts := make([]string, 0, len(pipelineStages))
	for _, stage := range pipelineStages {
		if d, ok := durations[stage]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", stage, d.Round(time.Millisecond)))
		}
	}
	return strings.Join(parts, " ")
}

// periodFailureFactor labels failures that abort a whole period before any
// individual factor outcome exists.
const periodFailureFactor = "*"

func periodFailureReason(err error) string {
	switch {
	case errors.Is(err, storageio.ErrInfra):
		return "storage_unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	default:
		return "internal"
	}
}
