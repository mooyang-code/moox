package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// 本地时序存储的后台维护：
//   - 跟进 Storage 的行变更日志：别处修正了历史数据（采集补齐、CSV 导入、人工修正），本地副本随之整段替换（设计 5.1）；
//   - 修正过已经算出过因子值的区间，要向后扩展到受影响的回看窗口重算（设计 5.3）。重算待办与数据同一个 batch 提交，
//     这里读出待办、在进程内用补算执行器重算，成功后清除。

// seriesFollower 是跟进变更与失效重算的后台循环。
type seriesFollower struct {
	data     *localdata.Data
	sets     seriesSets
	source   SourceDataset
	exec     chunkWithRunner
	periods  seriesCommitted
	active   func() bool
	interval time.Duration
	chunk    int
	now      func() time.Time
	// onGapped 在数据源的变更日志同步位点断档时调用（指标与告警）。
	onGapped func(src seriesstore.Source)
	// blocked 记录断档的数据源，用于避免每轮重复告警。
	blocked map[seriesstore.Source]bool
	// params 提供重算区间的冻结参数（可为 nil：全部按默认参数）。
	params paramIntervalSource
}

type seriesSets interface {
	SetByID(taskID string) (domain.EngineTask, SetLookup)
}

// seriesCommitted 返回任务已经算过的最后一期（水位已提交到的周期之前）；没有记录时 ok 为 false。
type seriesCommitted interface {
	CommittedThrough(taskID string) (time.Time, bool)
}

func (f *seriesFollower) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		f.tick(ctx)
	}
}

func (f *seriesFollower) tick(ctx context.Context) {
	if f.active != nil && !f.active() {
		return
	}
	res, err := f.data.Follow(ctx)
	if err != nil && ctx.Err() == nil {
		log.WarnContextf(ctx, "factor_series_follow_failed error=%v", err)
	}
	now := make(map[seriesstore.Source]bool, len(res.Gapped))
	for _, src := range res.Gapped {
		now[src] = true
		if !f.blocked[src] && f.onGapped != nil {
			f.onGapped(src)
		}
		if !f.blocked[src] {
			log.ErrorContextf(ctx, "factor_series_change_gap source=%s（变更日志断档：离线超过保留期或 Storage 节点更换，请执行 moox-factor-engine reconcile）", src)
		}
	}
	f.blocked = now
	f.recompute(ctx)
}

// recompute 处理各数据源待办的失效重算区间。同一个数据源可以被多个任务共用（各策略实例的私有任务）：
// 待办对每个任务都要重算，全部成功之后才清掉。
func (f *seriesFollower) recompute(ctx context.Context) {
	bySource := map[seriesstore.Source][]string{}
	var sources []seriesstore.Source
	for _, info := range f.data.Sources() {
		if _, seen := bySource[info.Source]; !seen {
			sources = append(sources, info.Source)
		}
		bySource[info.Source] = append(bySource[info.Source], info.TaskID)
	}
	for _, src := range sources {
		if ctx.Err() != nil || (f.active != nil && !f.active()) {
			return
		}
		pending, err := f.data.Store().Pending(src)
		if err != nil {
			log.WarnContextf(ctx, "factor_series_pending_read_failed source=%s error=%v", src, err)
			continue
		}
		if len(pending) == 0 {
			continue
		}
		done := true
		for _, taskID := range bySource[src] {
			if err := f.recomputeTask(ctx, taskID, pending); err != nil {
				done = false
				if ctx.Err() == nil && !errors.Is(err, recalcexec.ErrGated) {
					log.WarnContextf(ctx, "factor_series_recompute_failed task_id=%s error=%v", taskID, err)
				}
			}
		}
		if !done {
			continue
		}
		if err := f.data.Store().ClearPending(src, pending); err != nil {
			log.WarnContextf(ctx, "factor_series_pending_clear_failed source=%s error=%v", src, err)
		}
	}
}

func (f *seriesFollower) recomputeTask(ctx context.Context, taskID string, pending []seriesstore.PendingSpan) error {
	set, lookup := f.sets.SetByID(taskID)
	if lookup != SetFound {
		return nil // 任务已停用或删除：不影响同源的其他任务
	}
	step, err := seriesstore.Step(set.Set.Freq)
	if err != nil {
		return err
	}
	lookback := maxLookbackOf(set)
	cross := false
	for _, member := range set.Members {
		cross = cross || member.Factor.FactorType == domain.FactorTypeCrossSection
	}
	committed, hasCommitted := f.periods.CommittedThrough(taskID)

	// 失效范围向后扩展到受影响的回看窗口：[from, to + (L−1)×周期)；有连续水位时再截断到水位之前（水位之后的周期由实时与追赶用新数据算）。
	// 没有水位（任务还没有跑过实时周期）时水位不能代表「算过的范围」（可能做过人工补算），不截断，按请求的范围重算，待办不能丢。
	type job struct {
		from, to time.Time
		subjects map[string]struct{}
	}
	var jobs []*job
	for _, span := range pending {
		to := span.To.Add(time.Duration(lookback-1) * step)
		if hasCommitted && to.After(committed.Add(step)) {
			to = committed.Add(step)
		}
		if !span.From.Before(to) {
			continue // 这段还没算过：正常的实时与追赶会用新数据算
		}
		merged := false
		for _, j := range jobs {
			if !span.From.After(j.to) && !to.Before(j.from) {
				j.from, j.to = minTime(j.from, span.From), maxTime(j.to, to)
				j.subjects[span.Subject] = struct{}{}
				merged = true
				break
			}
		}
		if !merged {
			jobs = append(jobs, &job{from: span.From, to: to, subjects: map[string]struct{}{span.Subject: {}}})
		}
	}
	for _, j := range jobs {
		for start := j.from; start.Before(j.to); {
			end := start.Add(time.Duration(f.chunk) * step)
			if end.After(j.to) {
				end = j.to
			}
			if err := f.run(ctx, set, j.subjects, cross, start, end); err != nil {
				return err
			}
			start = end
		}
	}
	return nil
}

// run 在 [start, end) 上重算一批标的的全部因子（非只填充，覆盖已经算出的旧值）。截面因子用任务的全部标的重算。
func (f *seriesFollower) run(ctx context.Context, set domain.EngineTask, changed map[string]struct{}, cross bool, start, end time.Time) error {
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		if f.active != nil && !f.active() {
			return recalcexec.Selection{}, nil
		}
		current, lookup := f.sets.SetByID(set.Set.TaskID)
		if lookup != SetFound {
			return recalcexec.Selection{}, nil
		}
		columns, err := f.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		subjects := make([]string, 0, len(changed))
		if cross {
			all, err := setSubjects(ctx, f.source, current, nil)
			if err != nil {
				return recalcexec.Selection{}, err
			}
			subjects = all
		} else {
			for subject := range changed {
				subjects = append(subjects, subject)
			}
		}
		sort.Strings(subjects)
		// 失效重算的是已经算过的周期：按当时冻结的参数重算。
		var intervals map[string][]domain.ParamInterval
		if f.params != nil {
			if intervals, err = f.params.Intervals(ctx, current.Set.TaskID, start, end); err != nil {
				return recalcexec.Selection{}, fmt.Errorf("%w: read frozen params: %v", storageio.ErrInfra, err)
			}
		}
		return recalcexec.Selection{Set: current.Set, Members: current.Members, Subjects: subjects, Columns: columns, Intervals: intervals}, nil
	}
	_, err := f.exec.RunChunkWith(ctx, source, start, end, func(_ recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr == nil && outcome.Status == "degraded" {
			log.WarnContextf(ctx, "factor_series_recompute_degraded task_id=%s from=%s to=%s failed_subjects=%d", set.Set.TaskID,
				start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), len(outcome.FailedSubjects))
		}
	})
	return err
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
