package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

type observedStore struct {
	storageio.Store
	health *Health
}

func (s *observedStore) WriteRows(ctx context.Context, spaceID, datasetID string, rows []storageio.ResultRow) error {
	err := s.Store.WriteRows(ctx, spaceID, datasetID, rows)
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
	// periodTime 是轴上的周期：日志、指标与运行摘要按真实时间输出。
	axis, axisErr := periodclock.AxisFor(plan.Set.SpaceID, plan.Set.Freq)
	shown := periodclock.FormatPeriod(plan.Set.SpaceID, plan.Set.Freq, periodTime)
	r.health.StartRunner(plan.Set.TaskID, started)
	defer r.health.EndRunner(plan.Set.TaskID)
	log.InfoContextf(ctx, "factor_period_start task_id=%s period_time=%s mode=%d", plan.Set.TaskID, shown, plan.Mode)
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
		at, atErr := periodTime, axisErr
		if atErr == nil {
			at, atErr = axis.FromAxis(periodTime)
		}
		if atErr == nil {
			// PeriodLag 保留真实时间语义：这一期开盘到这次结束（成功或失败）的秒数，每期都更新。
			r.metrics.PeriodLag.WithLabelValues(taskID).Set(max(0, time.Since(at).Seconds()))
		}
		if atErr == nil && !periodTime.IsZero() && (err == nil || outcome.Status != "") {
			r.metrics.LastPeriodTime.WithLabelValues(taskID).Set(float64(at.Unix()))
			r.runs.record(taskID, axis, periodTime, status, outcome, time.Now())
		}
	}
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			reason := factor.Status
			if reason == "" {
				reason = "failed"
			}
			r.metrics.Failures.WithLabelValues(taskID, factor.MemberKey, reason).Inc()
			log.ErrorContextf(ctx, "factor_compute_failed task_id=%s member=%s factor_id=%s reason=%s", taskID, factor.MemberKey, factor.FactorID, reason)
		}
	}
	// 整截面没有算出（回归奇异、组数超限）：值是 NaN、照常写出，按原因计入失败指标，计数是目标区间内的截面数。
	for member, reasons := range outcome.Warnings {
		for reason, count := range reasons {
			r.metrics.Failures.WithLabelValues(taskID, member, reason).Add(float64(count))
			log.WarnContextf(ctx, "factor_cross_section_failed task_id=%s member=%s reason=%s sections=%d", taskID, member, reason, count)
		}
	}
	if err != nil {
		r.metrics.Failures.WithLabelValues(taskID, periodFailureFactor, periodFailureReason(err)).Inc()
		log.ErrorContextf(ctx, "factor_period_failed task_id=%s period_time=%s error=%v", taskID, shown, err)
	} else {
		log.InfoContextf(ctx, "factor_period_done task_id=%s period_time=%s status=%s rows=%d warming=%d duration=%s %s", taskID, shown, status, outcome.RowsWritten, outcome.Warming, time.Since(started), stageSummary(outcome.StageDurations))
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
