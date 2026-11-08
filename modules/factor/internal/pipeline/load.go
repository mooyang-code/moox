package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Load 取得一个计划的列式输入。数据都来自本地：实时周期由数据层先把本期标的从 Storage 同步到本地（只拉增量），
// 补算直接读本地存储；管线自己不再读 Storage 的窗口。
func (r *Runner) Load(ctx context.Context, plan Plan) (LoadResult, error) {
	if r == nil || r.data == nil {
		return LoadResult{}, errors.New("factor local data layer is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := LoadResult{Frames: make(map[string]*storageio.Frame)}
	subjects := uniqueSorted(plan.Available)
	failedTask := make(map[string]struct{}, len(plan.UpstreamFailed))
	for _, subject := range plan.UpstreamFailed {
		failedTask[subject] = struct{}{}
	}
	if len(subjects) == 0 {
		result.FailedSubjects = uniqueSorted(plan.UpstreamFailed)
		result.FailedSeries = append([]seriesset.Series(nil), plan.ExpectedSeries...)
		return result, nil
	}
	if !plan.TargetStart.Before(plan.TargetEnd) {
		return LoadResult{}, errors.New("factor read window must have start before end")
	}
	data, err := r.data.Load(ctx, plan)
	if err != nil {
		return LoadResult{}, err
	}
	result.View, result.WarmingSubjects = data.View, uniqueSorted(data.Warming)
	for subject, frame := range data.Frames {
		result.Frames[subject] = frame
	}
	for _, subject := range data.Failed {
		failedTask[subject] = struct{}{}
	}
	for _, subject := range result.WarmingSubjects {
		failedTask[subject] = struct{}{}
	}
	if plan.multi() {
		classifySeries(plan, data, &result)
		return result, nil
	}
	for _, subject := range subjects {
		if _, failed := failedTask[subject]; !failed {
			result.Available = append(result.Available, subject)
		}
	}
	for subject := range failedTask {
		result.FailedSubjects = append(result.FailedSubjects, subject)
	}
	result.FailedSubjects = uniqueSorted(result.FailedSubjects)
	return result, nil
}

// classifySeries 统一数据集：把期望序列分成本期可用与不可用的。标的整体失败（同步失败、预热中，failedSubject）或这条序列自己
// 不可用（上游失败、本期数据还没到）的序列不可用，其余可用；一个市场的缺数不连累另一个市场。
// 标的口径沿用原有定义：有不可用序列的标的记入 FailedSubjects，至少有一条可用序列的标的才是 Available。
func classifySeries(plan Plan, data DataResult, result *LoadResult) {
	failedSubject := make(map[string]struct{}, len(data.Failed)+len(result.WarmingSubjects))
	for _, subject := range data.Failed {
		failedSubject[subject] = struct{}{}
	}
	for _, subject := range result.WarmingSubjects {
		failedSubject[subject] = struct{}{}
	}
	unavailable := seriesSet(plan.UpstreamFailedSeries)
	for _, item := range data.FailedSeries {
		unavailable[item] = struct{}{}
	}
	for _, item := range plan.ExpectedSeries {
		if _, whole := failedSubject[item.SubjectID]; whole {
			result.FailedSeries = append(result.FailedSeries, item)
		} else if _, bad := unavailable[item]; bad {
			result.FailedSeries = append(result.FailedSeries, item)
		} else {
			result.AvailableSeries = append(result.AvailableSeries, item)
		}
	}
	result.Available = seriesSubjects(result.AvailableSeries)
	failed := append(seriesSubjects(result.FailedSeries), plan.UpstreamFailed...)
	for subject := range failedSubject {
		failed = append(failed, subject)
	}
	result.FailedSubjects = uniqueSorted(failed)
}

// WarmupStatuses 返回各任务内存窗口的载入状态。
func (r *Runner) WarmupStatuses() []WarmupStatus {
	if r == nil || r.data == nil {
		return nil
	}
	return r.data.WindowStatuses()
}

func readWindowStart(clock periodclock.Clock, plan Plan) (time.Time, error) {
	if plan.Mode != ModeRecalc || len(plan.Members) == 0 {
		return plan.TargetStart, nil
	}
	maxLookback := 1
	for _, member := range plan.Members {
		if member.Factor.LookbackPeriods > maxLookback {
			maxLookback = member.Factor.LookbackPeriods
		}
	}
	window, err := clock.Window(plan.TargetStart, plan.Set.Freq, maxLookback)
	if err != nil {
		return time.Time{}, fmt.Errorf("compute recalc read lookback: %w", err)
	}
	return window[0], nil
}

// ReadStart 返回计划需要的本地数据的起点：实时周期是回看窗口起点，补算是第一个目标周期往前一个回看窗口。
func ReadStart(clock periodclock.Clock, plan Plan) (time.Time, error) {
	return readWindowStart(clock, plan)
}
