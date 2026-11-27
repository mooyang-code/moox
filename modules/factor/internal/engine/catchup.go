package engine

import (
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/storagepb"
	"trpc.group/trpc-go/trpc-go/log"
)

// runCatchup 是连续水位的追赶循环：按间隔（保底，读回 Storage）或收到补写事件时，对落后或阻塞的任务继续推进。
func (r *Runtime) runCatchup(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.Watermark.PollInterval)
	defer ticker.Stop()
	for {
		var only string
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case only = <-r.advanceKick:
		}
		consumer := r.currentConsumer()
		if consumer == nil || !r.active() {
			continue
		}
		var behind []string
		for _, state := range r.continuity.States() {
			if state.Behind() && (only == "" || state.TaskID == only) {
				behind = append(behind, state.TaskID)
			}
		}
		advanceAll(ctx, behind, r.cfg.Pipeline.PeriodBudgetMax, consumer.Advance)
	}
}

// advanceAll 并发推进各任务的追赶：任务之间互不依赖，每个任务内部仍按期次串行（Advance 走任务自己的串行通道）。
// 每个任务有自己的时限，全部结束后返回；单个任务失败只记日志，不影响其他任务。
func advanceAll(ctx context.Context, taskIDs []string, budget time.Duration, advance func(context.Context, string) error) {
	var wg sync.WaitGroup
	for _, taskID := range taskIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			advanceCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			if err := advance(advanceCtx, taskID); err != nil {
				log.WarnContextf(ctx, "factor_watermark_catchup_failed task_id=%s error=%v", taskID, err)
			}
		}()
	}
	wg.Wait()
}

// onBackfilled 处理源 K 线补写事件（event.collector.kline.backfilled：补采分块确认、合成补写）：
// 记下补采确认行情源没有的标的或序列，标记这段周期已补齐（上游失败的待办不再等 Settle），唤醒追赶与跳过恢复。
// 被补写的行本身由本地数据跟随 Storage 变更日志取回并触发失效重算，这里不重复做。
func (r *Runtime) onBackfilled(spaceID string, payload *storagepb.CollectorKlineBackfilled) {
	// 同一个源数据集上的每个任务（各策略实例的私有任务）都要知道这段周期已补齐。
	sets, err := r.catalog.EnabledTasksByDataset(context.Background(), spaceID, payload.GetDatasetId(), payload.GetFrequency())
	if err != nil {
		return
	}
	realFrom, realTo := time.Unix(payload.GetStart(), 0).UTC(), time.Unix(payload.GetEnd(), 0).UTC()
	for _, engineTask := range sets {
		set := engineTask.Set
		// 事件里是真实时间的 [start, end)：换到轴上再记。
		axis, err := periodclock.AxisFor(set.SpaceID, set.Freq)
		if err != nil {
			continue
		}
		from, to, err := axisRange(axis, realFrom, realTo)
		if err != nil {
			log.Warnf("factor_kline_backfilled_invalid task_id=%s from=%s to=%s error=%v", set.TaskID, realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339), err)
			continue
		}
		r.filled.add(set.TaskID, from, to)
		// 把追赶目标推到补齐范围的最后一期（不超过当前最后一根已收盘的根）：实时取数整期失败时 Storage 不发周期事件，
		// 目标停在最后一次有事件的那一期；补采把后面补齐之后要靠这里把目标推上去，否则要等下一条实时事件
		// （港股收市后整期失败，2026-11-23 因子停在 15:00 那根，次日开盘才会补算）。整天重取的范围到次日零点，所以要设上限。
		// 上限与恢复追赶相同（catchupTarget）：扣掉采集宽限，刚收盘、还在结算的那根不算。
		if latest, err := catchupTarget(axis, r.filled.now().UTC()); err == nil {
			if last := minTime(to.Add(-axis.Step()), latest); !last.Before(from) {
				if err := r.continuity.NoteTarget(set.TaskID, last); err != nil {
					log.Warnf("factor_kline_backfilled_target_failed task_id=%s error=%v", set.TaskID, err)
				}
			}
		}
		for _, absent := range payload.GetAbsent() {
			absentFrom, absentTo, err := axisRange(axis, time.Unix(absent.GetStart(), 0).UTC(), time.Unix(absent.GetEnd(), 0).UTC())
			if err != nil {
				log.Warnf("factor_kline_backfilled_absent_invalid task_id=%s subject=%s error=%v", set.TaskID, absent.GetSubjectId(), err)
				continue
			}
			r.continuity.NoteAbsent(set.TaskID, absentFrom, absentTo, axis.Step(), []string{absent.GetSubjectId()})
		}
		log.Infof("factor_kline_backfilled task_id=%s from=%s to=%s origin=%s absent=%d", set.TaskID, realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339), payload.GetOrigin(), len(payload.GetAbsent()))
		select {
		case r.advanceKick <- set.TaskID:
		default:
		}
	}
	if r.kickGap != nil {
		r.kickGap()
	}
}

// axisRange 把真实时间的 [from, to) 换到任务的时间轴上。
func axisRange(axis periodclock.Axis, from, to time.Time) (time.Time, time.Time, error) {
	axisFrom, err := axis.ToAxis(from.UTC())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	axisTo, err := axis.AxisEnd(to.UTC())
	return axisFrom, axisTo, err
}

// filledPeriods 记录收到补写事件的周期范围（1 小时后过期）。
type filledPeriods struct {
	mu     sync.Mutex
	now    func() time.Time
	ranges map[string][]filledRange
}

type filledRange struct {
	from, to, until time.Time
}

func newFilledPeriods(now func() time.Time) *filledPeriods {
	return &filledPeriods{now: now, ranges: map[string][]filledRange{}}
}

func (f *filledPeriods) add(taskID string, from, to time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ranges[taskID] = append(f.ranges[taskID], filledRange{from: from, to: to, until: f.now().Add(time.Hour)})
}

// covers 判断该任务这一期是否已收到补写事件。
func (f *filledPeriods) covers(taskID string, period time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	kept := f.ranges[taskID][:0]
	found := false
	for _, item := range f.ranges[taskID] {
		if now.After(item.until) {
			continue
		}
		kept = append(kept, item)
		if !period.Before(item.from) && period.Before(item.to) {
			found = true
		}
	}
	f.ranges[taskID] = kept
	return found
}

// retain 仅保留 keep 中的任务补写记录，清理已删除任务的残留。
func (f *filledPeriods) retain(keep map[string]struct{}) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for taskID := range f.ranges {
		if _, ok := keep[taskID]; !ok {
			delete(f.ranges, taskID)
		}
	}
}
