package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 恢复追赶（设计 2.2）：引擎暂停之后恢复，先在追赶门禁下把每个任务暂停期间缺失的数据补进来、把历史周期补算出来，
// 全部追平后才放行回到实时计算。
//
//	① 同步本地数据：Data.Prepare 做 Follow（已有范围的修正）+ Pull（新增尾部）+ 重载内存窗口，每次恢复只做一次；
//	② 显式建立追赶目标：暂停期间的实时事件在连续水位目标更新之前就被 ACK，目标会停在暂停前，不能等下一条事件；
//	③ 连续水位追赶：复用现有的追赶循环（Advance 在追赶门禁下放行），补算暂停区间每个周期的因子并发布历史结果；
//	④ 追平：连续水位追到目标并且没有阻塞。
//
// 变更日志断档（暂停超过保留期或 Storage 身份变化）时增量同步无法追上：任务检查的“变更同步”一项不通过，任务被排除出追赶、
// 不再计算，需要离线维护（reconcile / backfill）。

// catchupGrace 是追赶目标之前留的宽限：最新一根刚收盘时采集还在路上，不把它算进追赶目标，避免一直追不平。
const catchupGraceMax = time.Minute

type catchupTracker struct {
	mu   sync.Mutex
	jobs map[string]*catchupJob
}

// catchupJob 是一个任务这一次恢复的本地数据同步进展。
type catchupJob struct {
	since time.Time
	state string // syncing | synced
	err   string
}

func (t *catchupTracker) get(taskID string, since time.Time) *catchupJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job := t.jobs[taskID]; job != nil && job.since.Equal(since) {
		return job
	}
	return nil
}

func (t *catchupTracker) put(taskID string, job *catchupJob) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jobs == nil {
		t.jobs = map[string]*catchupJob{}
	}
	t.jobs[taskID] = job
}

func (t *catchupTracker) update(taskID string, since time.Time, mutate func(*catchupJob)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job := t.jobs[taskID]; job != nil && job.since.Equal(since) {
		mutate(job)
	}
}

// snapshot 读取 job 当前的同步状态（后台同步协程会并发修改它，必须在锁内读）。
func (t *catchupTracker) snapshot(job *catchupJob) (state, errText string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return job.state, job.err
}

func (t *catchupTracker) drop(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, taskID)
}

// catchupHook 是生命周期的 Catchup 钩子：引擎是活动引擎、满足追赶前置条件（本次恢复之后目录同步成功、参与追赶的任务引用的
// 自定义算子模块全部就绪），并且参与追赶的任务都追平，才算追平。参与者见 catchupMembers：除本地窗口外还有不通过项的任务
// 本来就不计算，不参与本次追赶、也不挡住引擎放行（检查通过之后由连续水位从断点补算）；只是窗口没载入的任务照常参与，
// 由 catchupSet 先载入窗口，载入并重查通过之前报未追平。
func (r *Runtime) catchupHook(ctx context.Context, since time.Time) (lifecycle.Catchup, error) {
	out := lifecycle.Catchup{Since: since, CaughtUp: true}
	if !r.active() {
		out.CaughtUp, out.BlockedBy, out.Message = false, "引擎", "因子目录还没有载入，或另一个引擎持有租约"
		return out, nil
	}
	members := r.catchupMembers()
	if reason := r.catchupPreconditionFor(members); reason != "" {
		out.CaughtUp, out.BlockedBy, out.Message = false, "追赶前置条件", reason
		return out, nil
	}
	now := time.Now().UTC()
	for _, set := range members {
		taskID := set.Set.TaskID
		cu := r.catchupSet(ctx, taskID, since, now)
		out.Done += cu.Done
		out.Total += cu.Total
		if cu.Target.After(out.Target) {
			out.Target = cu.Target
		}
		if !cu.CaughtUp && out.CaughtUp {
			out.CaughtUp, out.BlockedBy, out.Message = false, "因子任务 "+taskID, cu.Message
			if cu.BlockedBy != "" {
				out.Message = cu.BlockedBy + "：" + cu.Message
			}
		}
	}
	return out, nil
}

// catchupTarget 是追赶目标（轴上）：扣掉采集宽限后最新一根已收盘的周期。
func catchupTarget(axis periodclock.Axis, now time.Time) (time.Time, error) {
	grace := min(axis.Step()/2, catchupGraceMax)
	end, err := axis.AxisEnd(now.Add(-grace))
	if err != nil {
		return time.Time{}, err
	}
	return end.Add(-axis.Step()), nil
}

func (r *Runtime) catchupSet(ctx context.Context, taskID string, since, now time.Time) lifecycle.Catchup {
	out := lifecycle.Catchup{Since: since}
	set, lookup := r.catalog.SetByID(taskID)
	if lookup != SetFound {
		out.Message = "任务已不在目录里"
		return out
	}
	if reason := r.catchupPrecondition(taskID); reason != "" {
		out.BlockedBy = "追赶前置条件"
		out.Message = reason
		return out
	}
	// 变更日志断档不在这里判断：catchupHook 只对参与者调用这里，断档的任务已被 catchupMembers 排除。

	// ① 本地数据同步：每次恢复只做一次，放在后台，进度由 job 状态报告；同步会载入窗口并立即重查任务。
	job := r.catchups.get(taskID, since)
	if job == nil {
		job = &catchupJob{since: since, state: "syncing"}
		r.catchups.put(taskID, job)
		go r.syncForCatchup(taskID, since)
	}
	state, errText := r.catchups.snapshot(job)
	switch {
	case errText != "":
		out.Message = "同步本地数据失败：" + errText
		r.catchups.drop(taskID) // 下一次评估重试
		return out
	case state != "synced":
		out.Message = "同步本地数据中"
		return out
	}

	// ② 显式建立追赶目标。追赶在轴上进行；生命周期给的 since 与上报的目标是真实时间。
	axis, err := periodclock.AxisFor(set.Set.SpaceID, set.Set.Freq)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	target, err := catchupTarget(axis, now)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	step := axis.Step()
	axisSince, err := axis.AxisEnd(since)
	if err != nil {
		out.Message = err.Error()
		return out
	}
	out.Target = realPeriod(axis, target)
	// 没有连续水位（不消费事件的运行时，例如测试）：没有需要补齐的周期，同步完成即追平。
	if r.continuity == nil {
		out.CaughtUp = true
		return out
	}
	if err := r.continuity.NoteTarget(taskID, target); err != nil {
		out.Message = "建立追赶目标失败：" + err.Error()
		return out
	}

	// ③ 唤醒连续水位追赶循环。
	select {
	case r.advanceKick <- taskID:
	default:
	}

	// ④ 追平判定与进度。
	for _, state := range r.continuity.States() {
		if state.TaskID != taskID {
			continue
		}
		total := int64(target.Sub(axisSince) / step)
		done := int64(state.CommittedThrough.Sub(axisSince) / step)
		out.Total, out.Done = max(total, 0), min(max(done, 0), max(total, 0))
		if !state.BlockedSince.IsZero() {
			out.Message = fmt.Sprintf("水位卡在 %s：%s", axis.Format(state.BlockedPeriod), state.BlockedReason)
			return out
		}
		out.CaughtUp = !state.CommittedThrough.Before(target)
		return out
	}
	// 任务从未跑过实时周期（没有水位记录）：连续性从第一次实时事件起保证，同步完成即追平。
	out.CaughtUp = true
	return out
}

// syncForCatchup 同步一个任务的本地数据（Follow + Pull + 重载窗口）。
func (r *Runtime) syncForCatchup(taskID string, since time.Time) {
	base := r.appCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, preloadTimeout)
	defer cancel()
	err := r.preload.Refresh(ctx, taskID)
	r.catchups.update(taskID, since, func(job *catchupJob) {
		if err != nil {
			job.err = err.Error()
			return
		}
		job.state = "synced"
	})
	if err != nil {
		log.WarnContextf(ctx, "factor_catchup_sync_failed task_id=%s error=%v", taskID, err)
		return
	}
	log.InfoContextf(ctx, "factor_catchup_synced task_id=%s since=%s", taskID, since.UTC().Format(time.RFC3339))
}
