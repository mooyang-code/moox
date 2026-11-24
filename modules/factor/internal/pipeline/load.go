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
	if len(plan.Available) == 0 {
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
	classifySubjects(plan, data, &result)
	return result, nil
}

// LoadSubjects 给管理端试算用：按 plan.Available 读入并分类，与实时、补算同一个判断。
func (r *Runner) LoadSubjects(ctx context.Context, plan Plan) (Plan, LoadResult, error) {
	if r == nil || r.data == nil {
		return plan, LoadResult{}, errors.New("factor local data layer is required")
	}
	if !plan.TargetStart.Before(plan.TargetEnd) {
		return plan, LoadResult{}, errors.New("factor read window must have start before end")
	}
	data, err := r.data.Load(ctx, plan)
	if err != nil {
		return plan, LoadResult{}, err
	}
	result := LoadResult{Frames: data.Frames, View: data.View, WarmingSubjects: uniqueSorted(data.Warming)}
	if result.Frames == nil {
		result.Frames = make(map[string]*storageio.Frame)
	}
	classifySubjects(plan, data, &result)
	return plan, result, nil
}

// classifySubjects 把期望标的分成本期可用与不可用的：同步失败、本期数据还没到、预热中或上游失败的标的不可用，记入 FailedSubjects；
// 其余期望标的可用（Available）。
func classifySubjects(plan Plan, data DataResult, result *LoadResult) {
	failed := make(map[string]struct{}, len(data.Failed)+len(result.WarmingSubjects)+len(plan.UpstreamFailed))
	for _, group := range [][]string{data.Failed, result.WarmingSubjects, plan.UpstreamFailed} {
		for _, subject := range group {
			failed[subject] = struct{}{}
		}
	}
	for _, subject := range uniqueSorted(plan.Expected) {
		if _, bad := failed[subject]; !bad {
			result.Available = append(result.Available, subject)
		}
	}
	all := make([]string, 0, len(failed))
	for subject := range failed {
		all = append(all, subject)
	}
	result.FailedSubjects = uniqueSorted(all)
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
