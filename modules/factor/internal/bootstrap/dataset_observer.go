package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
)

// engineView is the part of the engine hub the observer reads: the catalog the
// engine computes and the engine's latest heartbeat.
type engineView interface {
	Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineTask, error)
	Engine(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error)
}

// factorDatasetObserver turns the engine heartbeat into factor dataset
// metrics, so Monitor's dataset freshness checks cover factor computation the
// way they cover collection. The engine runs on an operator machine without
// metrics credentials; it already reports each set's latest live period to
// the manager. A set whose periods stop advancing, including because the
// engine stopped, goes stale in Monitor.
type factorDatasetObserver struct {
	engine  engineView
	metrics *report.DatasetMetrics
	clock   periodclock.Clock
	now     func() time.Time
	// notify 触发一次补报，默认 report.NotifyOutputAdvanced，测试可替换。
	notify func()
	// observed is the latest period recorded per set, so a heartbeat repeating
	// the same period is not counted as another run. Timer ticks may overlap.
	mu       sync.Mutex
	observed map[string]observedPeriod
	// graced 是本进程里已经处理过首期等待宽限的任务（见 graceNewTasksLocked）。
	graced map[string]bool
	// blocked 是各任务连续水位阻塞的起始时间（只记正在阻塞的任务），按最近一次引擎心跳整份替换；采集指标时据此判断停滞。
	blocked map[string]blockedWatermark
}

// blockedWatermark 是一个任务连续水位阻塞的空间与起始时刻。
type blockedWatermark struct {
	spaceID string
	since   time.Time
}

// watermarkStallAfter 是因子任务连续水位阻塞多久上报关注项：不自动跳过，交给人工（手动补采、确认缺失）。
const watermarkStallAfter = 30 * time.Minute

// watermarkAttention 列出连续水位阻塞超过 30 分钟的任务：<空间>/<任务> 自 hh:mm 起。阻塞起始时刻以引擎心跳为准，同一次阻塞内不变。
func (o *factorDatasetObserver) watermarkAttention(now time.Time) map[string][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var stalled []string
	for taskID, item := range o.blocked {
		if now.Sub(item.since) > watermarkStallAfter {
			stalled = append(stalled, fmt.Sprintf("%s/%s 自 %s 起", item.spaceID, taskID, report.AttentionTime(item.since)))
		}
	}
	if len(stalled) == 0 {
		return nil
	}
	sort.Strings(stalled)
	// 合成一条：对象按序在前、处理提示在后（多条会被重新排序）。
	detail := "前面有还没补到的周期，不会自动跳过：" + strings.Join(stalled, "；") + "。请检查补采，或确认行情源缺失"
	return map[string][]string{attentionWatermarkStalled: {detail}}
}

// observedPeriod 是一个任务已记录的最新一期与它的结果等级。同一期只接受结果变好的升级（缺口补算补齐了预热中或空洞跳过的项）：
// 从不正式（预热中、出错，不推进产出水位）到正式，或者从不完整到成功。
type observedPeriod struct {
	period int64
	rank   int
}

// resultRank 给一期的结果分级：0 不推进产出水位（预热中、出错），1 正式但不完整，2 成功。
func resultRank(observation report.DatasetObservation) int {
	switch {
	case observation.OutputWatermark.IsZero():
		return 0
	case observation.Result == "success":
		return 2
	default:
		return 1
	}
}

func newFactorDatasetObserver(engine engineView, metrics *report.DatasetMetrics) *factorDatasetObserver {
	return &factorDatasetObserver{engine: engine, metrics: metrics, clock: periodclock.Continuous{}, now: time.Now, observed: make(map[string]observedPeriod), graced: make(map[string]bool), notify: report.NotifyOutputAdvanced}
}

func (o *factorDatasetObserver) observe(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _, sets, err := o.engine.Snapshot(ctx, "")
	if err != nil {
		o.metrics.ObserveInventoryRefreshError()
		return fmt.Errorf("load factor tasks: %w", err)
	}
	// 引擎上报的暂停任务：结果数据集不再被期望持续产出，Monitor 对它的新鲜度检查（计算、写入、视图）一并跳过。
	// 读不到引擎状态时不当作暂停，沿用原来的期望清单。
	_, status, engineKnown, engineErr := o.engine.Engine(ctx)
	paused := make(map[string]bool)
	if engineErr == nil && engineKnown {
		for _, runner := range status.Runners {
			if runner.Paused {
				paused[runner.TaskID] = true
			}
		}
	}
	keys := make(map[string]report.DatasetKey, len(sets))
	expected := make([]report.DatasetExpectation, 0, len(sets))
	for _, set := range sets {
		if !set.ResultReady || set.Set.ResultDatasetID == "" {
			continue
		}
		interval, err := o.clock.Duration(set.Set.Freq)
		if err != nil {
			continue
		}
		key := report.DatasetKey{SpaceID: set.Set.SpaceID, DatasetID: set.Set.ResultDatasetID, Freq: set.Set.Freq}
		keys[set.Set.TaskID] = key
		// 源数据集在历史补齐中的任务不出结果：当作暂停处理，结果数据集置“暂停”期望并带原因 history_seeding，Monitor 不做新鲜度告警、页面显示“待生效”。
		item := report.DatasetExpectation{Key: key, Interval: interval, Paused: paused[set.Set.TaskID]}
		if set.SourceSeeding {
			item.Paused, item.PauseReason = true, report.PauseHistorySeeding
		}
		expected = append(expected, item)
	}
	if err := o.metrics.ReplaceExpected(expected); err != nil {
		return fmt.Errorf("replace expected factor datasets: %w", err)
	}
	// 引擎状态不明（刚重启、还没收到心跳）时不做宽限：这时看不到已有任务的运行记录，会把它们误当成新任务。
	if engineErr != nil || !engineKnown {
		return engineErr
	}
	o.graceNewTasksLocked(keys, paused, status.RecentRuns)
	blocked := make(map[string]blockedWatermark)
	for _, item := range status.Watermarks {
		key, known := keys[item.TaskID]
		if known && !item.BlockedSince.IsZero() {
			blocked[item.TaskID] = blockedWatermark{spaceID: key.SpaceID, since: item.BlockedSince.UTC()}
		}
	}
	o.blocked = blocked
	finishedAt := o.now().UTC()
	for _, run := range status.RecentRuns {
		key, expected := keys[run.TaskID]
		// 暂停的任务不在期望清单里（ObserveRun 要求数据集在期望清单里），它暂停前的最后一期不再上报。
		if !expected || paused[run.TaskID] || run.LastPeriodTime <= 0 {
			continue
		}
		// 到达时间用引擎写完这一期的时刻（补算补齐时是补齐的时刻）；引擎没有给出时（重启前的期次）退回观测时刻。
		observedAt := finishedAt
		if !run.CompletedAt.IsZero() {
			observedAt = run.CompletedAt.UTC()
		}
		observation := report.DatasetObservation{Key: key, Result: datasetResult(run), FinishedAt: observedAt}
		// Warming results are not formal, so they do not advance the output.
		if run.WarmingSubjects == 0 && observation.Result != "error" {
			observation.OutputWatermark = time.Unix(run.LastPeriodTime, 0).UTC()
		}
		previous, seen := o.observed[run.TaskID]
		next := observedPeriod{period: run.LastPeriodTime, rank: resultRank(observation)}
		if seen && (next.period < previous.period || (next.period == previous.period && next.rank <= previous.rank)) {
			continue
		}
		o.observed[run.TaskID] = next
		if err := o.metrics.ObserveRun(observation); err != nil {
			return fmt.Errorf("observe factor task %s: %w", run.TaskID, err)
		}
		// 同一期在产出水位已推进之后才升级为成功（空洞跳过的项补齐了）：水位不再前进，ObserveRun 不会补报，
		// 这里主动补报一次，Monitor 立刻看到成功，不等 30 秒的定时上报。到达时间仍是第一次正式产出的时刻。
		if seen && next.period == previous.period && previous.rank > 0 {
			o.notify()
		}
	}
	return nil
}

// graceNewTasksLocked 给刚进入期望清单、还没有任何一期结果的任务一个首期等待宽限（与策略侧 restoreObservations 的首期等待宽限同一做法）：
// 结果数据集建好就进入期望清单，Monitor 看到它没有任何运行会立即判为“尚未上报”，而这个判断排在休市豁免之前；
// 第一次运行要等第一根 K 线收盘，A 股收盘后、周末建的任务会整夜告警。这里以当前时刻记一次“空”运行（只刷新最近运行
// 与最近成功时间，不推进产出水位），之后由 Monitor 的休市豁免与新鲜度检查接管。
//
// 指标只在进程内存里：factor-mgr 重启后每个任务首次进入期望清单时都会重新写这一条，不会回到“尚未上报”。只在引擎状态已知时做：
// 引擎已经上报过这个任务的一期时不需要宽限（随后的运行记录直接覆盖）；暂停的任务不在期望清单里。
// 7×24 市场的新任务也会多一条 empty，下一分钟的第一期结果随即覆盖，无害。调用方持有 o.mu。
func (o *factorDatasetObserver) graceNewTasksLocked(keys map[string]report.DatasetKey, paused map[string]bool, runs []domain.TaskRunSummary) {
	ran := make(map[string]bool, len(runs))
	for _, run := range runs {
		if run.LastPeriodTime > 0 {
			ran[run.TaskID] = true
		}
	}
	for taskID := range o.graced {
		if _, ok := keys[taskID]; !ok {
			delete(o.graced, taskID)
		}
	}
	for taskID, key := range keys {
		if o.graced[taskID] || paused[taskID] {
			continue
		}
		o.graced[taskID] = true
		if _, seen := o.observed[taskID]; seen || ran[taskID] {
			continue
		}
		if err := o.metrics.ObserveRun(report.DatasetObservation{Key: key, Result: "empty", FinishedAt: o.now().UTC()}); err != nil {
			log.Warnf("factor_dataset_grace_failed task_id=%s error=%v", taskID, err)
		}
	}
}

func datasetResult(run domain.TaskRunSummary) string {
	switch {
	case run.LastStatus == "failed":
		return "error"
	case run.LastStatus == "complete" && run.WarmingSubjects == 0:
		return "success"
	case partialByDesign(run):
		return "partial"
	default:
		return "incomplete"
	}
}

// partialByDesign 判断这一期是否只因“数据还没攒够”而不完整：失败的标的都是预热中的（结果不正式、列在失败标的里），
// 没有别的失败。数据重置、新建因子任务、重启后的第一期都是这种情况，生产者在正常工作；Monitor 据此显示“积累中”而不告警。
// 失败标的多于预热中的标的就是意外的不完整。
func partialByDesign(run domain.TaskRunSummary) bool {
	return run.WarmingSubjects > 0 && len(run.FailedSubjects) <= int(run.WarmingSubjects)
}
