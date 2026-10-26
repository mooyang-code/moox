package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
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
	notify func(spaceID string)
	// observed is the latest period recorded per set, so a heartbeat repeating
	// the same period is not counted as another run. Timer ticks may overlap.
	mu       sync.Mutex
	observed map[string]observedPeriod
	// blocked 是各任务连续水位阻塞的起始时间（0 表示没有阻塞），Monitor 据此对停滞超过 30 分钟告警。
	blocked *prometheus.GaugeVec
}

// watermarkBlockedMetric 是因子任务连续水位阻塞起始时间的指标。
const watermarkBlockedMetric = "moox_factor_task_watermark_blocked_since_timestamp_seconds"

func newWatermarkBlockedGauge(registerer prometheus.Registerer) *prometheus.GaugeVec {
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: watermarkBlockedMetric, Help: "因子任务连续水位阻塞开始的 Unix 时间，0 表示没有阻塞。"}, []string{"space_id", "task_id"})
	if err := registerer.Register(gauge); err != nil {
		if existing, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return existing.ExistingCollector.(*prometheus.GaugeVec)
		}
	}
	return gauge
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
	return &factorDatasetObserver{engine: engine, metrics: metrics, clock: periodclock.Continuous{}, now: time.Now, observed: make(map[string]observedPeriod), notify: report.NotifyOutputAdvanced}
}

func (o *factorDatasetObserver) observe(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _, sets, err := o.engine.Snapshot(ctx, "")
	if err != nil {
		o.metrics.ObserveInventoryRefreshError()
		return fmt.Errorf("load factor tasks: %w", err)
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
		expected = append(expected, report.DatasetExpectation{Key: key, Interval: interval})
	}
	if err := o.metrics.ReplaceExpected(expected); err != nil {
		return fmt.Errorf("replace expected factor datasets: %w", err)
	}
	_, status, ok, err := o.engine.Engine(ctx)
	if err != nil || !ok {
		return err
	}
	if o.blocked != nil {
		for _, item := range status.Watermarks {
			key, known := keys[item.TaskID]
			if !known {
				continue
			}
			value := 0.0
			if !item.BlockedSince.IsZero() {
				value = float64(item.BlockedSince.Unix())
			}
			o.blocked.WithLabelValues(key.SpaceID, item.TaskID).Set(value)
		}
	}
	finishedAt := o.now().UTC()
	for _, run := range status.RecentRuns {
		key, expected := keys[run.TaskID]
		if !expected || run.LastPeriodTime <= 0 {
			continue
		}
		// 到达时间用引擎写完这一期的时刻（补算补齐时是补齐的时刻）；引擎没有给出时（重启前的期次）退回观测时刻。
		observedAt := finishedAt
		if run.CompletedAt > 0 {
			observedAt = time.Unix(run.CompletedAt, 0).UTC()
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
			o.notify(key.SpaceID)
		}
	}
	return nil
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

// partialByDesign 判断这一期是否只因“数据还没攒够”而不完整：失败的标的都是回看窗口空洞（窗口里缺 K 线，保护逻辑不出值，
// 见 FactorPeriodState.GapSubjects）或预热中（结果不正式、列在失败标的里），没有别的失败。数据重置、新建因子任务、重启后
// 的第一期都是这种情况，生产者在正常工作；Monitor 据此显示“积累中”而不告警。有任何空洞与预热之外的失败就是意外的不完整。
func partialByDesign(run domain.TaskRunSummary) bool {
	gap := map[string]struct{}{}
	for _, factor := range run.Factors {
		for _, subject := range factor.GapSubjects {
			gap[subject] = struct{}{}
		}
	}
	if len(gap) == 0 && run.WarmingSubjects == 0 {
		return false
	}
	other := 0
	for _, subject := range run.FailedSubjects {
		if _, ok := gap[subject]; !ok {
			other++
		}
	}
	return other <= int(run.WarmingSubjects)
}
