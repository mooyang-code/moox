package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
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
	failedSet := make(map[string]struct{}, len(plan.UpstreamFailed))
	for _, subject := range plan.UpstreamFailed {
		failedSet[subject] = struct{}{}
	}
	if len(subjects) == 0 {
		result.FailedSubjects = uniqueSorted(plan.UpstreamFailed)
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
		failedSet[subject] = struct{}{}
	}
	for _, subject := range result.WarmingSubjects {
		failedSet[subject] = struct{}{}
	}
	for _, subject := range subjects {
		if _, failed := failedSet[subject]; !failed {
			result.Available = append(result.Available, subject)
		}
	}
	for subject := range failedSet {
		result.FailedSubjects = append(result.FailedSubjects, subject)
	}
	result.FailedSubjects = uniqueSorted(result.FailedSubjects)
	return result, nil
}

// WarmupStatuses 返回各集合内存窗口的载入状态。
func (r *Runner) WarmupStatuses() []WarmupStatus {
	if r == nil || r.data == nil {
		return nil
	}
	return r.data.WindowStatuses()
}

func readWindowStart(clock periodclock.Clock, plan Plan) (time.Time, error) {
	if plan.Mode != ModeRecalc || len(plan.Factors) == 0 {
		return plan.TargetStart, nil
	}
	maxLookback := 1
	for _, factor := range plan.Factors {
		if factor.LookbackPeriods > maxLookback {
			maxLookback = factor.LookbackPeriods
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
