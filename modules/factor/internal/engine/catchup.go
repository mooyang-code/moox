package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

// runCatchup 是连续水位的追赶循环：按间隔（保底，读回 Storage）或收到补齐通知时，对落后或阻塞的任务继续推进。
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

// onFilled 处理补齐通知：记下补采确认缺失的标的，标记这段周期已补齐（上游失败的待办不再等 Settle），
// 唤醒追赶与跳过恢复。
func (r *Runtime) onFilled(filled []*factorpb.PeriodsFilled, kickGap func()) {
	for _, item := range filled {
		// 同一个源数据集上的每个任务（各策略实例的私有任务）都要知道这段周期已补齐。
		sets, err := r.catalog.EnabledTasksByDataset(context.Background(), item.GetSpaceId(), item.GetDatasetId(), item.GetFrequency())
		if err != nil {
			continue
		}
		for _, engineTask := range sets {
			set := engineTask.Set
			// 补齐通知来自心跳响应，是真实时间的 [from, to)：换到轴上再记。
			axis, err := periodclock.AxisFor(set.SpaceID, set.Freq)
			if err != nil {
				continue
			}
			realFrom, realTo := time.Unix(item.GetFrom(), 0).UTC(), time.Unix(item.GetTo(), 0).UTC()
			from, err := axis.ToAxis(realFrom)
			if err == nil {
				var to time.Time
				if to, err = axis.AxisEnd(realTo); err == nil {
					r.noteFilled(set, axis, from, to, item.GetAbsentSubjects(), absentSeriesOf(item.GetAbsentSeries()))
				}
			}
			if err != nil {
				log.Warnf("factor_periods_filled_invalid task_id=%s from=%s to=%s error=%v", set.TaskID, realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339), err)
				continue
			}
			log.Infof("factor_periods_filled task_id=%s from=%s to=%s absent=%s absent_series=%d", set.TaskID, realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339), strings.Join(item.GetAbsentSubjects(), ","), len(item.GetAbsentSeries()))
			select {
			case r.advanceKick <- set.TaskID:
			default:
			}
		}
	}
	if kickGap != nil {
		kickGap()
	}
}

// noteFilled 在轴上记下一段已补齐的周期 [from, to)：补采确认缺失的标的或序列、上游失败的待办不再等 Settle。
// 按序列确认缺失的通知（统一数据集）只表示那一条序列没有，不是整段补齐，所以只记确认缺失、不记补齐范围：
// 同一次补采的整段补齐通知（不带确认缺失）已经记过范围，只补了永续的通知不会误记现货。
func (r *Runtime) noteFilled(set domain.FactorTask, axis periodclock.Axis, from, to time.Time, absent []string, absentSeries []seriesset.Series) {
	r.continuity.NoteAbsent(set.TaskID, from, to, axis.Step(), absent, absentSeries)
	if len(absentSeries) > 0 && len(absent) == 0 {
		return
	}
	r.filled.add(set.TaskID, from, to)
}

// absentSeriesOf 把通知里的序列名单转成内部类型。
func absentSeriesOf(values []*factorpb.SeriesRef) []seriesset.Series {
	out := make([]seriesset.Series, 0, len(values))
	for _, value := range values {
		out = append(out, seriesset.Series{SubjectID: value.GetSubjectId(), SeriesTag: value.GetSeriesTag()})
	}
	return out
}

// filledPeriods 记录收到补齐通知的周期范围（1 小时后过期）。
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

// covers 判断该任务这一期是否已收到补齐通知。
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
