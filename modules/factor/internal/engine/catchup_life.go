package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 恢复追赶（设计 2.2）：集合暂停之后恢复，先在追赶门禁下把暂停期间缺失的数据补进来、把历史周期补算出来，
// 追平后才放行回到实时计算。
//
//	① 同步本地数据：Data.Prepare 做 Follow（已有范围的修正）+ Pull（新增尾部）+ 重载内存窗口，每次恢复只做一次；
//	② 显式建立追赶目标：暂停期间的实时事件在连续水位目标更新之前就被 ACK，目标会停在暂停前，不能等下一条事件；
//	③ 连续水位追赶：复用现有的追赶循环（Advance 在追赶门禁下放行），补算暂停区间每个周期的因子并发布历史结果；
//	④ 追平：连续水位追到目标并且没有阻塞。
//
// 变更日志断档（暂停超过保留期或 Storage 身份变化）时增量同步无法追上，报告阻塞原因，需要离线维护（reconcile / backfill）。

// catchupGrace 是追赶目标之前留的宽限：最新一根刚收盘时采集还在路上，不把它算进追赶目标，避免一直追不平。
const catchupGraceMax = time.Minute

type catchupTracker struct {
	mu   sync.Mutex
	jobs map[string]*catchupJob
}

// catchupJob 是一个集合这一次恢复的本地数据同步进展。
type catchupJob struct {
	since time.Time
	state string // syncing | synced
	err   string
}

func (t *catchupTracker) get(setID string, since time.Time) *catchupJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job := t.jobs[setID]; job != nil && job.since.Equal(since) {
		return job
	}
	return nil
}

func (t *catchupTracker) put(setID string, job *catchupJob) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs == nil {
		t.jobs = map[string]*catchupJob{}
	}
	t.jobs[setID] = job
}

func (t *catchupTracker) update(setID string, since time.Time, mutate func(*catchupJob)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job := t.jobs[setID]; job != nil && job.since.Equal(since) {
		mutate(job)
	}
}

func (t *catchupTracker) drop(setID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, setID)
}

// catchupHook 是生命周期的 Catchup 钩子：模块自身没有数据要追赶（由各集合决定），集合走 catchupSet。
func (r *Runtime) catchupHook(ctx context.Context, ref lifecycle.Ref, since time.Time) (lifecycle.Catchup, error) {
	if ref == (lifecycle.Ref{}) {
		return lifecycle.Catchup{CaughtUp: true}, nil
	}
	return r.catchupSet(ctx, ref.ID, since, time.Now().UTC()), nil
}

// catchupTarget 是追赶目标：最新一根已收盘（扣掉采集宽限）的周期。
func catchupTarget(clock periodclock.Clock, freq string, now time.Time) (time.Time, time.Duration, error) {
	step, err := clock.Duration(freq)
	if err != nil {
		return time.Time{}, 0, err
	}
	grace := min(step/2, catchupGraceMax)
	// Align 只校验对齐、不取整，先向下取整到周期边界。
	aligned, err := clock.Align(now.Add(-grace).Truncate(step), freq)
	if err != nil {
		return time.Time{}, 0, err
	}
	return aligned.Add(-step), step, nil
}

func (r *Runtime) catchupSet(ctx context.Context, setID string, since, now time.Time) lifecycle.Catchup {
	out := lifecycle.Catchup{Since: since}
	set, lookup := r.catalog.SetByID(setID)
	if lookup != SetFound {
		out.Message = "集合已不在目录里"
		return out
	}
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}
	if err := r.data.Blocked(src); err != nil {
		out.BlockedBy = "变更日志断档"
		out.Message = err.Error() + "：增量同步追不上，需要离线维护（moox-factor-engine reconcile / backfill）"
		return out
	}

	// ① 本地数据同步：每次恢复只做一次，放在后台，进度由 job 状态报告。
	job := r.catchups.get(setID, since)
	if job == nil {
		job = &catchupJob{since: since, state: "syncing"}
		r.catchups.put(setID, job)
		go r.syncForCatchup(setID, since)
	}
	switch {
	case job.err != "":
		out.Message = "同步本地数据失败：" + job.err
		r.catchups.drop(setID) // 下一次评估重试
		return out
	case job.state != "synced":
		out.Message = "同步本地数据中"
		return out
	}

	// ② 显式建立追赶目标。
	clock, err := periodclock.ForSpace(set.Set.SpaceID)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	target, step, err := catchupTarget(clock, set.Set.Freq, now)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	out.Target = target
	if err := r.continuity.NoteTarget(setID, target); err != nil {
		out.Message = "建立追赶目标失败：" + err.Error()
		return out
	}

	// ③ 唤醒连续水位追赶循环。
	select {
	case r.advanceKick <- setID:
	default:
	}

	// ④ 追平判定与进度。
	for _, state := range r.continuity.States() {
		if state.SetID != setID {
			continue
		}
		total := int64(target.Sub(since) / step)
		done := int64(state.CommittedThrough.Sub(since) / step)
		out.Total, out.Done = max(total, 0), min(max(done, 0), max(total, 0))
		if !state.BlockedSince.IsZero() {
			out.Message = fmt.Sprintf("水位卡在 %s：%s", state.BlockedPeriod.UTC().Format(time.RFC3339), state.BlockedReason)
			return out
		}
		out.CaughtUp = !state.CommittedThrough.Before(target)
		return out
	}
	// 集合从未跑过实时周期（没有水位记录）：连续性从第一次实时事件起保证，同步完成即追平。
	out.CaughtUp = true
	return out
}

// syncForCatchup 同步一个集合的本地数据（Follow + Pull + 重载窗口）。
func (r *Runtime) syncForCatchup(setID string, since time.Time) {
	base := r.appCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, preloadTimeout)
	defer cancel()
	err := r.preload.Refresh(ctx, setID)
	r.catchups.update(setID, since, func(job *catchupJob) {
		if err != nil {
			job.err = err.Error()
			return
		}
		job.state = "synced"
	})
	if err != nil {
		log.WarnContextf(ctx, "factor_catchup_sync_failed set_id=%s error=%v", setID, err)
		return
	}
	log.InfoContextf(ctx, "factor_catchup_synced set_id=%s since=%s", setID, since.UTC().Format(time.RFC3339))
}
