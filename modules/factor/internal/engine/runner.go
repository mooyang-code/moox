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

	// carried 是每个成员连续沿用参数的期数（集合/别名 → 期数），实时周期按冻结来源更新。
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
	r.health.StartLane(plan.Set.SetID, started)
	defer r.health.EndLane(plan.Set.SetID)
	log.InfoContextf(ctx, "factor_period_start set_id=%s period_time=%s mode=%d", plan.Set.SetID, periodTime.UTC().Format(time.RFC3339), plan.Mode)
	outcome, err := r.inner.Run(ctx, plan)
	setID := plan.Set.SetID
	for stage, duration := range outcome.StageDurations {
		r.metrics.PeriodDuration.WithLabelValues(setID, stage).Observe(duration.Seconds())
	}
	status := outcome.Status
	if err != nil {
		status = "failed"
	}
	if status == "" {
		status = "complete"
	}
	if plan.Mode == pipeline.ModeLive {
		r.metrics.PeriodTotal.WithLabelValues(setID, status).Inc()
		r.metrics.PeriodLag.WithLabelValues(setID).Set(max(0, time.Since(periodTime).Seconds()))
		if !periodTime.IsZero() && (err == nil || outcome.Status != "") {
			r.metrics.LastPeriodTime.WithLabelValues(setID).Set(float64(periodTime.Unix()))
			r.runs.record(setID, periodTime, status, outcome, time.Now())
		}
		r.recordParams(setID, outcome)
	}
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			reason := factor.Status
			if reason == "" {
				reason = "failed"
			}
			r.metrics.Failures.WithLabelValues(setID, factor.Member, reason).Inc()
			log.ErrorContextf(ctx, "factor_compute_failed set_id=%s member=%s factor_id=%s reason=%s", setID, factor.Member, factor.FactorID, reason)
		}
	}
	if err != nil {
		r.metrics.Failures.WithLabelValues(setID, periodFailureFactor, periodFailureReason(err)).Inc()
		log.ErrorContextf(ctx, "factor_period_failed set_id=%s period_time=%s error=%v", setID, periodTime.UTC().Format(time.RFC3339), err)
	} else {
		log.InfoContextf(ctx, "factor_period_done set_id=%s period_time=%s status=%s rows=%d warming=%d duration=%s %s", setID, periodTime.UTC().Format(time.RFC3339), status, outcome.RowsWritten, outcome.Warming, time.Since(started), stageSummary(outcome.StageDurations))
	}
	return outcome, err
}

// recordParams 按每个成员这一期冻结参数的来源计数，并维护连续沿用的期数：沿用（含离线沿用）加一，
// 新提交生效时归零；默认值（从未提交过）不算沿用。
func (r *measuredRunner) recordParams(setID string, outcome pipeline.Outcome) {
	r.carriedMu.Lock()
	defer r.carriedMu.Unlock()
	if r.carried == nil {
		r.carried = map[string]int{}
	}
	for _, state := range outcome.Factors {
		if state.ParamsSource == "" {
			continue
		}
		r.metrics.ParamFreezes.WithLabelValues(setID, state.ParamsSource).Inc()
		key := setID + "/" + state.Member
		switch state.ParamsSource {
		case domain.ParamsCarried, domain.ParamsCarriedOffline:
			r.carried[key]++
		default:
			r.carried[key] = 0
		}
		r.metrics.ParamCarriedPeriods.WithLabelValues(setID, state.Member).Set(float64(r.carried[key]))
	}
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
