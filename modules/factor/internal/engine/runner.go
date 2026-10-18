package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

type observedStore struct {
	storageio.Store
	health *Health
}

// earliestPeriodReader 是存储客户端读取标的最早一根 K 线的能力；窗口空洞检查用它区分新上市与真正的空洞。
// observedStore 嵌入的是 storageio.Store 接口，不含这个方法，必须显式转发，否则 pipeline 里的类型断言永远失败。
type earliestPeriodReader interface {
	EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error)
}

func (s *observedStore) EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error) {
	reader, ok := s.Store.(earliestPeriodReader)
	if !ok {
		return time.Time{}, false, errors.New("storage client does not support earliest period reads")
	}
	return reader.EarliestPeriod(ctx, spaceID, datasetID, freq, subjects, columns)
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
}

func (r *measuredRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	started := time.Now()
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
	}
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			reason := factor.Status
			if reason == "" {
				reason = "failed"
			}
			r.metrics.Failures.WithLabelValues(setID, factor.FactorID, reason).Inc()
			log.ErrorContextf(ctx, "factor_compute_failed set_id=%s factor_id=%s reason=%s", setID, factor.FactorID, reason)
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
