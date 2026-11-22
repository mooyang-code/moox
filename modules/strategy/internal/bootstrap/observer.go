package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/paramsubmit"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	moduleStage       = "evaluate"
	moduleHealthCheck = "strategy-targets"
)

// accumulatingReasons 是“数据还没攒够”的跳过原因：View 的历史不足以判断 min_age_bars，或上游因子这期被跳过（例如截面因子的全集缺数）。
// 数据重置、新建因子任务之后必然出现一段，数据攒够自动恢复。只要还在积累期上限之内，这类跳过记为 partial（Monitor 显示“积累中”，
// 不告警，也不计模块失败）；超过上限仍然不足，说明不是在积累（例如 min_age_bars 超过 View 保留根数），回到失败处理。
var accumulatingReasons = map[string]bool{
	input.SkipHistoryInsufficient: true,
	readiness.ReasonFactorSkipped: true,
}

// minAccumulationLimit 是积累期上限的下限：上游因子的回看窗口可达两三天（1h 因子任务），min_age_bars 对应的时长更短时也要给足。
const minAccumulationLimit = 72 * time.Hour

// 新实例因子预热的积累上限：warmupMinimum 与 warmupPeriods 个周期取较大者。
const (
	warmupMinimum = 30 * time.Minute
	warmupPeriods = 20
)

// failureReasons 是计入模块健康检查失败的跳过原因；其余跳过是业务上的"本期不能求值"，只通过实例的数据集新鲜度告警。
// market_missing（绑定两个因子任务的实例里另一个市场的数据没到）属于后者：数据未到，不是模块故障。
var failureReasons = map[string]bool{
	trigger.SkipInfraRetryExhausted: true,
	input.SkipConfigError:           true,
	input.SkipHistoryInsufficient:   true,
	input.SkipAmbiguousSeries:       true,
}

var metricLabelPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// instanceObserver 把每个启用实例登记为 Monitor 的期望数据集（间隔见 runIntervalLocked），每期上报一次运行；
// 同时累计 moox_strategy_period_total{instance_id,result,reason}。
type instanceObserver struct {
	store    *store.Store
	datasets *report.DatasetMetrics
	module   *report.ModuleMetrics
	periods  *prometheus.CounterVec
	// paramSubmits 统计可变成员参数投递的终态；paramStale 是各实例的参数投递是否已连续多期没有送达。
	paramSubmits *prometheus.CounterVec
	paramStale   *prometheus.GaugeVec
	// staleAlertPeriods 是触发 paramStale 的连续未送达期数（0 表示不告警）。
	staleAlertPeriods int
	staleExported     map[string]string
	now               func() time.Time
	logf              func(format string, args ...any)

	mu       sync.Mutex
	expected map[string]report.DatasetExpectation
	// halted 非空且返回 true 时（模块暂停、恢复追赶中）期望清单带暂停标记：实例被有意停下，Monitor 不对它做新鲜度检查
	//（与因子任务暂停时的做法一致）。
	halted func() bool
	// lastBarEnd 与 lastOkBarEnd 是每个实例最近处理的一根、最近一个 ok 的一根的 bar_end，是 A 股实例期望间隔的基准。
	lastBarEnd   map[string]time.Time
	lastOkBarEnd map[string]time.Time
	// restored 是每个实例在本进程里已经从库里恢复过的会话 ID：载入了期望间隔的基准并补报了观测（见 restoreLocked）。
	// 处理周期时写入的基准只是库里基准的一部分，不能据此认为已恢复。模块暂停中、读库失败时不记，下一次刷新再试；
	// 实例移出启用清单时清除，重新启用（换了会话）后重新恢复。
	restored map[string]string
	// accumulatingSince 是每个实例本轮“数据积累中”的起点（第一次因积累类原因跳过的时刻，出现 ok 即清除）：
	// 只在进程内记录，重启后重新计时，所以积累期上限最长随重启顺延——上限是兜底，不是精确的计时器。
	accumulatingSince map[string]time.Time
}

func newInstanceObserver(repo *store.Store, registerer prometheus.Registerer, logf func(string, ...any)) (*instanceObserver, error) {
	datasets, err := report.NewDatasetMetrics(registerer, "strategy")
	if err != nil {
		return nil, fmt.Errorf("创建策略数据集指标失败：%w", err)
	}
	module, err := report.NewModuleMetrics(registerer, "strategy", report.HealthCheckIDsForModule("strategy"))
	if err != nil {
		return nil, fmt.Errorf("创建策略模块指标失败：%w", err)
	}
	periods := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_strategy_period_total", Help: "策略实例处理的周期数，按结果与跳过原因分类。"}, []string{"instance_id", "result", "reason"})
	if err := registerer.Register(periods); err != nil {
		return nil, fmt.Errorf("注册策略周期指标失败：%w", err)
	}
	cancelled := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_strategy_target_cancelled_total", Help: "被取消的策略目标数，按原因分类（superseded、expired、inactive、rejected）；其中发布期间被取消、随后确认已发出的另计于 moox_strategy_target_sent_after_cancel_total。"}, []string{"reason"})
	if err := registerer.Register(cancelled); err != nil {
		return nil, fmt.Errorf("注册策略目标取消指标失败：%w", err)
	}
	sentAfterCancel := prometheus.NewCounter(prometheus.CounterOpts{Name: "moox_strategy_target_sent_after_cancel_total", Help: "发布期间被取消、随后确认已发出并改记 sent 的策略目标数（取消计数不回退）。"})
	if err := registerer.Register(sentAfterCancel); err != nil {
		return nil, fmt.Errorf("注册策略目标取消后发出指标失败：%w", err)
	}
	// 预先登记各原因，没有发生过的原因也有值为 0 的序列。
	for _, reason := range store.CancelReasons {
		cancelled.WithLabelValues(reason)
	}
	repo.SetCancelObserver(func(reason string, count int64) {
		cancelled.WithLabelValues(reason).Add(float64(count))
	})
	repo.SetSentAfterCancelObserver(sentAfterCancel.Inc)
	paramSubmits := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moox_strategy_param_submit_total", Help: "策略投递的可变成员参数终态数，按结果分类（delivered、rejected）。"}, []string{"result"})
	if err := registerer.Register(paramSubmits); err != nil {
		return nil, fmt.Errorf("注册因子参数提交指标失败：%w", err)
	}
	for _, status := range []string{paramsubmit.ResultDelivered, paramsubmit.ResultRejected} {
		paramSubmits.WithLabelValues(status)
	}
	paramStale := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: paramStaleMetric, Help: "实例有可变成员、并且最近连续多期的结果都没有把参数送达因子侧为 1，否则为 0。"}, []string{"space_id", "instance_id"})
	if err := registerer.Register(paramStale); err != nil {
		return nil, fmt.Errorf("注册因子参数投递滞后指标失败：%w", err)
	}
	return &instanceObserver{store: repo, datasets: datasets, module: module, periods: periods, paramSubmits: paramSubmits, paramStale: paramStale, staleExported: map[string]string{}, now: time.Now, logf: logf, expected: map[string]report.DatasetExpectation{}, lastBarEnd: map[string]time.Time{}, lastOkBarEnd: map[string]time.Time{}, restored: map[string]string{}, accumulatingSince: map[string]time.Time{}}, nil
}

// datasetKey 由实例派生 Monitor 的数据集标识；不合法的空间或周期返回 false。数据集标识只接受小写：实例 ID 区分大小写，
// 只差大小写的两个实例（Abc 与 abc）不能映射到同一个标识（登记会整批失败），所以只有本身已是合法小写形式的 ID 才直接使用，
// 其余（含大写、含非法字符、过长）用 ID 的哈希。
func datasetKey(instance store.Instance, bar string) (report.DatasetExpectation, bool) {
	if !metricLabelPattern.MatchString(instance.SpaceID) {
		return report.DatasetExpectation{}, false
	}
	freq, err := frequency.Parse(bar)
	if err != nil || freq.NominalDuration() <= 0 {
		return report.DatasetExpectation{}, false
	}
	datasetID := "strategy_" + instance.InstanceID
	if !metricLabelPattern.MatchString(datasetID) {
		sum := sha256.Sum256([]byte(instance.InstanceID))
		datasetID = "strategy_" + hex.EncodeToString(sum[:8])
	}
	return report.DatasetExpectation{Key: report.DatasetKey{SpaceID: instance.SpaceID, DatasetID: datasetID, Freq: string(freq)}, Interval: freq.NominalDuration()}, true
}

// maxDefinitionNameRunes 是实例展示名里定义名称的字符上限：告警标题还要带实例 ID 前 8 位和检查类型，过长的定义名截断。
const maxDefinitionNameRunes = 40

// instanceDisplayName 是实例给人看的名称「定义名称（实例 ID 前 8 位）」，与控制台策略页一致：同一定义的多个实例靠
// 实例 ID 前 8 位区分，也能在策略页按它找到实例。定义名称未知时返回空，Monitor 退回数据集 ID。
func instanceDisplayName(definitionName, instanceID string) string {
	definitionName = strings.TrimSpace(definitionName)
	if definitionName == "" {
		return ""
	}
	if runes := []rune(definitionName); len(runes) > maxDefinitionNameRunes {
		definitionName = strings.TrimSpace(string(runes[:maxDefinitionNameRunes-1])) + "…"
	}
	shortID := []rune(strings.TrimSpace(instanceID))
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return definitionName + "（" + string(shortID) + "）"
}

// definitionNames 读取未删除定义的名称（键为策略 ID）。只用于给 Monitor 的数据集检查起名，读取失败时返回空表，
// 期望清单照常登记（不带名称），下一轮刷新再取。
func (o *instanceObserver) definitionNames(ctx context.Context) map[string]string {
	definitions, err := o.store.ListDefinitions(ctx)
	if err != nil {
		o.log("读取策略定义名称失败，本轮数据集不带名称：%v", err)
		return nil
	}
	names := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		names[definition.StrategyID] = definition.Name
	}
	return names
}

// refresh 用当前启用实例重建期望数据集清单；停用的实例随之移出。
func (o *instanceObserver) refresh(ctx context.Context) error {
	enabled := true
	instances, err := o.store.ListInstances(ctx, "", &enabled)
	if err != nil {
		o.datasets.ObserveInventoryRefreshError()
		return fmt.Errorf("读取启用实例：%w", err)
	}
	o.mu.Lock()
	halted := o.halted != nil && o.halted()
	o.mu.Unlock()
	points := o.loadRestorePoints(ctx, instances, halted)
	o.observeParamDelivery(ctx, instances)
	definitionNames := o.definitionNames(ctx)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mergeBasesLocked(points)
	next := make(map[string]report.DatasetExpectation, len(instances))
	for _, instance := range instances {
		resolved, err := input.ParseResolved(instance.ResolvedJSON)
		if err != nil {
			continue
		}
		if expectation, ok := datasetKey(instance, resolved.Bar); ok {
			expectation.Interval = o.runIntervalLocked(instance.InstanceID, resolved.Calendar, resolved.Bar, expectation.Interval)
			expectation.Name = instanceDisplayName(definitionNames[instance.StrategyID], instance.InstanceID)
			expectation.Paused = halted
			next[instance.InstanceID] = expectation
		}
	}
	if err := o.datasets.ReplaceExpected(expectations(next)); err != nil {
		return err
	}
	o.expected = next
	o.restoreLocked(instances, points, next)
	for id := range o.lastBarEnd {
		if _, ok := next[id]; !ok {
			delete(o.lastBarEnd, id)
		}
	}
	for id := range o.lastOkBarEnd {
		if _, ok := next[id]; !ok {
			delete(o.lastOkBarEnd, id)
		}
	}
	for id := range o.restored {
		if _, ok := next[id]; !ok {
			delete(o.restored, id)
		}
	}
	return nil
}

// restorePoint 是实例在本进程、本会话第一次刷新时从库里读到的处理记录（不分会话）与会话的启用时刻。
type restorePoint struct {
	session   string
	enabledAt time.Time
	processed store.ResultMark
	ok        store.ResultMark
	// seenBar 是合并库里的基准之前，本进程 ObservePeriod 已经记下的最近一根的 bar_end（零表示本进程还没上报过这个实例）。
	seenBar time.Time
}

// loadRestorePoints 为本进程、本会话还没恢复过的实例读库，供 mergeBasesLocked 与 restoreLocked 使用；读库不占观察器的锁。
// 模块暂停中（期望带暂停标记，Monitor 不收它的运行）整体留到下一次刷新：暂停期间不需要期望间隔，基准也一并推迟载入。
// 读取失败的实例留到下一次刷新再试。
func (o *instanceObserver) loadRestorePoints(ctx context.Context, instances []store.Instance, halted bool) map[string]*restorePoint {
	if halted {
		return nil
	}
	points := make(map[string]*restorePoint)
	for _, instance := range instances {
		if instance.SessionID == nil {
			continue
		}
		id, session := instance.InstanceID, *instance.SessionID
		o.mu.Lock()
		done := o.restored[id] == session
		o.mu.Unlock()
		if done {
			continue
		}
		processed, ok, err := o.store.LatestMarks(ctx, id)
		if err != nil {
			o.log("读取实例 %s 最近处理的周期失败，下一次刷新再试：%v", id, err)
			continue
		}
		record, err := o.store.GetSession(ctx, session)
		if err != nil {
			o.log("读取实例 %s 的会话失败，下一次刷新再试：%v", id, err)
			continue
		}
		points[id] = &restorePoint{session: session, enabledAt: record.CreatedAt.UTC(), processed: processed, ok: ok}
	}
	return points
}

// mergeBasesLocked 把库里最近处理的一根与最近一个 ok 的一根并入期望间隔的基准（股票市场实例的期望间隔以它们计算，否则周末
// 重启后周一跳过一根就会报 success stale）。与进程内已记下的取较晚者：重启后第一批积压事件可能先于第一次刷新到达。
// 合并前先记下进程内的值，restoreLocked 据此判断本进程是否已经上报过这个实例。调用方持有 o.mu。
func (o *instanceObserver) mergeBasesLocked(points map[string]*restorePoint) {
	for id, point := range points {
		point.seenBar = o.lastBarEnd[id]
		if point.processed.BarEnd.After(point.seenBar) {
			o.lastBarEnd[id] = point.processed.BarEnd
		}
		if point.ok.BarEnd.After(o.lastOkBarEnd[id]) {
			o.lastOkBarEnd[id] = point.ok.BarEnd
		}
	}
}

// restoreLocked 在实例于本进程、本会话第一次刷新时给 Monitor 补一次观测。Monitor 的最近运行、最近成功与输出水位只来自生产者
// 进程内存里的指标，策略进程重启或实例刚启用后都是空的；休市期间（股票市场周末）又没有周期可跑，不补就一直是“尚未上报”。
// 每个实例每个会话只处理一次（restored），上报失败只记日志：期望刚登记成功，失败只可能是观测本身不合法，重试也不会成功。
// 调用方持有 o.mu，期望清单已登记。
func (o *instanceObserver) restoreLocked(instances []store.Instance, points map[string]*restorePoint, expected map[string]report.DatasetExpectation) {
	for _, instance := range instances {
		id := instance.InstanceID
		point := points[id]
		if point == nil {
			continue
		}
		o.restored[id] = point.session
		expectation, ok := expected[id]
		if !ok {
			continue
		}
		resolved, err := input.ParseResolved(instance.ResolvedJSON)
		if err != nil {
			continue
		}
		for _, observation := range o.restoreObservations(id, point, expectation.Key, resolved.Calendar, resolved.Bar) {
			if err := o.datasets.ObserveRun(observation); err != nil {
				o.log("补报实例 %s 的观测失败：%v", id, err)
			}
		}
	}
}

// restoreObservations 返回要补报的观测，分三种情况：
//
//   - 从没处理过周期（刚启用）：首期等待宽限。登记期望后 Monitor 里还没有这个数据集的运行，会立即判为“尚未上报”，而首个事件
//     要等下一个 bar 收线之后才到。以启用（会话创建）时刻记一次“空”运行，Monitor 的运行与成功新鲜度从启用时刻起算。起点固定在
//     启用时刻而不是当前时间：反复重启延后不了告警。
//   - 停用后重新启用、本次会话还没处理到启用时刻已收线的那一根：Monitor 里的输出水位还停在停用前，停用期间的周期不会补跑，
//     却会被数成应到未到（股票市场周末重新启用也照样报运行过期）。同样以启用时刻记一次空运行，并把输出水位推到启用时刻已收线的
//     最近一根（bar_start），Monitor 只等启用之后收线的根。
//   - 其余是实例早已在跑、只是进程重启：按 ObservePeriod 的口径补报库里的记录。最近一个 ok 的一根记 success（输入、输出水位都是
//     这根的 bar_start）；最近处理的一根更晚时再记一次非成功运行（输入水位是它的 bar_start，跳过原因属于积累类时记 partial，
//     其余记 incomplete）。完成时间取记录的写入时间，拿不到时用这根的收线时间。本进程已经上报过这个实例（积压事件先于第一次
//     刷新到达）时不补：DatasetMetrics 的最近运行、最近成功按每次上报直接覆盖，补报更早的记录会把时间往回拨。
func (o *instanceObserver) restoreObservations(id string, point *restorePoint, key report.DatasetKey, calendar, bar string) []report.DatasetObservation {
	processed := point.processed.BarEnd
	if point.seenBar.After(processed) {
		processed = point.seenBar
	}
	grace := report.DatasetObservation{Key: key, Result: "empty", FinishedAt: point.enabledAt}
	if processed.IsZero() {
		return []report.DatasetObservation{grace}
	}
	period, err := input.ClosedPeriod(calendar, bar, point.enabledAt)
	if err != nil {
		o.log("计算实例 %s 启用时刻的周期失败，不补报观测：%v", id, err)
		return nil
	}
	if period.BarEnd.After(processed) {
		grace.OutputWatermark = period.StorageStart
		return []report.DatasetObservation{grace}
	}
	if !point.seenBar.IsZero() {
		return nil
	}
	var out []report.DatasetObservation
	mark := func(item store.ResultMark, result string) {
		boundary, err := input.FromBarEnd(calendar, bar, item.BarEnd)
		if err != nil {
			o.log("计算实例 %s 周期 %s 的起点失败，不补报：%v", id, item.BarEnd.Format(time.RFC3339), err)
			return
		}
		finishedAt := item.CreatedAt
		if finishedAt.IsZero() {
			finishedAt = item.BarEnd
		}
		observation := report.DatasetObservation{Key: key, Result: result, FinishedAt: finishedAt.UTC(), InputWatermark: boundary.StorageStart}
		if result == "success" {
			observation.OutputWatermark = boundary.StorageStart
		}
		out = append(out, observation)
	}
	if !point.ok.BarEnd.IsZero() {
		mark(point.ok, "success")
	}
	if point.processed.BarEnd.After(point.ok.BarEnd) {
		result := "incomplete"
		if accumulatingReasons[point.processed.SkipReason] {
			result = "partial"
		}
		mark(point.processed, result)
	}
	return out
}

// 与 config/setup/dataset-health-policy.yaml 一致：Monitor 在最近一次运行之后 run_missed_intervals 个间隔没有新的运行
// 报 run stale，在最近一次成功之后 success_missed_intervals 个间隔没有新的成功报 success stale。
const (
	runMissedIntervals     = 2
	successMissedIntervals = 3
)

// runIntervalLocked 返回实例的期望运行间隔。crypto 是 bar 时长。股票市场相邻交易日之间隔着周末与长假，按交易日折算：
// 运行口径取“最近处理的一根到其后第 2 根”的间隔的一半，成功口径取“最近一个 ok 的一根到其后第 3 根”的间隔的三分之一，
// 两者取较大者——Monitor 的两种滞后都乘同一个间隔，只按运行口径时，周末后第一根跳过会让间隔缩短、立即报 success
// stale。还没处理过时以最近闭合一根的上一根为基准（偏宽），还没有 ok 时成功口径与运行口径同一基准。调用方持有 o.mu。
func (o *instanceObserver) runIntervalLocked(instanceID, calendar, bar string, nominal time.Duration) time.Duration {
	if !input.SessionCalendar(calendar) {
		return nominal
	}
	reference, ok := o.lastBarEnd[instanceID]
	if !ok {
		period, err := input.ClosedPeriod(calendar, bar, o.now())
		if err != nil {
			return nominal
		}
		if reference, err = input.AdvanceBarEnd(calendar, bar, period.BarEnd, -1); err != nil {
			return nominal
		}
	}
	success, ok := o.lastOkBarEnd[instanceID]
	if !ok {
		success = reference
	}
	interval := nominal
	if after, err := input.AdvanceBarEnd(calendar, bar, reference, runMissedIntervals); err == nil {
		interval = max(interval, after.Sub(reference)/runMissedIntervals)
	}
	if after, err := input.AdvanceBarEnd(calendar, bar, success, successMissedIntervals); err == nil {
		interval = max(interval, after.Sub(success)/successMissedIntervals)
	}
	return interval
}

func expectations(values map[string]report.DatasetExpectation) []report.DatasetExpectation {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]report.DatasetExpectation, 0, len(ids))
	for _, id := range ids {
		out = append(out, values[id])
	}
	return out
}

// ObservePeriod 记录一个处理周期：ok → success（推进输出水位），skipped → incomplete。水位用这根 K 线在存储里的 data_time
// （开盘时间），与 Storage、因子的水位同一口径，Monitor 按交易日历算它的收线时间和到达延迟。
func (o *instanceObserver) ObservePeriod(ctx context.Context, instance store.Instance, bar string, barStart, barEnd time.Time, result, reason string) {
	o.periods.WithLabelValues(instance.InstanceID, result, reason).Inc()
	warming := o.warmingUp(ctx, instance, result, reason)
	o.mu.Lock()
	defer o.mu.Unlock()
	if barEnd.After(o.lastBarEnd[instance.InstanceID]) {
		o.lastBarEnd[instance.InstanceID] = barEnd
	}
	if result == store.StatusOK && barEnd.After(o.lastOkBarEnd[instance.InstanceID]) {
		o.lastOkBarEnd[instance.InstanceID] = barEnd
	}
	expectation, ok := o.expected[instance.InstanceID]
	if !ok {
		if expectation, ok = datasetKey(instance, bar); !ok {
			return
		}
		if resolved, err := input.ParseResolved(instance.ResolvedJSON); err == nil {
			expectation.Interval = o.runIntervalLocked(instance.InstanceID, resolved.Calendar, bar, expectation.Interval)
		}
		next := make(map[string]report.DatasetExpectation, len(o.expected)+1)
		for id, value := range o.expected {
			next[id] = value
		}
		next[instance.InstanceID] = expectation
		if err := o.datasets.ReplaceExpected(expectations(next)); err != nil {
			o.log("登记实例 %s 的期望数据集失败：%v", instance.InstanceID, err)
			return
		}
		o.expected = next
	}
	finishedAt := o.now().UTC()
	observation := report.DatasetObservation{Key: expectation.Key, Result: "incomplete", FinishedAt: finishedAt, InputWatermark: barStart}
	accumulating := false
	if result == store.StatusOK {
		observation.Result = "success"
		observation.OutputWatermark = barStart
		delete(o.accumulatingSince, instance.InstanceID)
	} else if (accumulatingReasons[reason] || warming) && o.accumulatingLocked(instance, bar, finishedAt, warming) {
		observation.Result = "partial"
		accumulating = true
	}
	if err := o.datasets.ObserveRun(observation); err != nil {
		o.log("上报实例 %s 的周期运行失败：%v", instance.InstanceID, err)
	}
	switch {
	case result == store.StatusOK:
		_ = o.module.ObserveRun(moduleStage, "success", moduleHealthCheck, finishedAt)
	case accumulating:
		// 数据积累中：策略模块在正常处理事件，只是数据还没攒够无需求值，不是模块故障；记一次成功，模块健康检查
		// 才不会因为长时间没有“成功”而报过期。
		_ = o.module.ObserveRun(moduleStage, "success", moduleHealthCheck, finishedAt)
	case failureReasons[reason]:
		_ = o.module.ObserveRun(moduleStage, "error", moduleHealthCheck, finishedAt)
	case reason == readiness.ReasonFactorChanged:
		// 因子定义变化需要人工重新启用，计入模块失败以便提醒。
		_ = o.module.ObserveRun(moduleStage, "error", moduleHealthCheck, finishedAt)
	}
}

// accumulatingLocked 记下（首次出现时）实例进入积累期的时刻，并判断是否仍在积累期上限之内。上限取 min_age_bars 根 K 线
// 对应的时长与 minAccumulationLimit 的较大者再加一小时：数据按时间攒，再久还不够就不是在积累。调用方持有 o.mu。
func (o *instanceObserver) accumulatingLocked(instance store.Instance, bar string, at time.Time, warming bool) bool {
	since, ok := o.accumulatingSince[instance.InstanceID]
	if !ok {
		since = at
		o.accumulatingSince[instance.InstanceID] = since
	}
	limit := minAccumulationLimit
	if warming {
		// 新实例前几根因子为空，几十分钟就够了，太久还缺说明真的缺数。
		limit = warmupMinimum
		if expectation, valid := datasetKey(instance, bar); valid {
			limit = max(limit, warmupPeriods*expectation.Interval)
		}
		// 股票市场攒够 warmupPeriods 根要跨午休、隔夜或周末：按交易日历往后数，而不是按墙钟。
		if resolved, err := input.ParseResolved(instance.ResolvedJSON); err == nil && input.SessionCalendar(resolved.Calendar) {
			if period, err := input.ClosedPeriod(resolved.Calendar, bar, since); err == nil {
				if after, err := input.AdvanceBarEnd(resolved.Calendar, bar, period.BarEnd, warmupPeriods); err == nil {
					limit = max(limit, after.Sub(since))
				}
			}
		}
		return at.Sub(since) <= limit
	}
	if expectation, valid := datasetKey(instance, bar); valid {
		if resolved, err := input.ParseResolved(instance.ResolvedJSON); err == nil && resolved.LongestAge() > 0 {
			limit = max(limit, time.Duration(resolved.LongestAge())*expectation.Interval)
		}
	}
	return at.Sub(since) <= limit+time.Hour
}

// warmingUp 报告这次 too_many_missing 发生在本次会话第一个 ok 之前：实例刚启用，前几根因子还是空的。
// 按会话查库，策略重启后判断不变；放在加锁之前，查询不占观察器的锁。查询失败时按原样告警。
func (o *instanceObserver) warmingUp(ctx context.Context, instance store.Instance, result, reason string) bool {
	if result == store.StatusOK || reason != engine.SkipTooManyMissing || o.store == nil || instance.SessionID == nil {
		return false
	}
	_, found, err := o.store.LatestOk(ctx, instance.InstanceID, *instance.SessionID)
	return err == nil && !found
}

func (o *instanceObserver) log(format string, args ...any) {
	if o.logf != nil {
		o.logf(format, args...)
	}
}

// paramStaleMetric 是参数投递滞后告警的指标名（设计 6.5、S7）。
const paramStaleMetric = "moox_strategy_param_delivery_stale"

// SetHalted 设置“模块是否被有意停下”的判断（生命周期控制器的 Halted）。
func (o *instanceObserver) SetHalted(halted func() bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.halted = halted
}

// SetStaleAlertPeriods 设置触发参数投递滞后告警的连续未送达期数；0 表示不告警。
func (o *instanceObserver) SetStaleAlertPeriods(periods int) { o.staleAlertPeriods = periods }

// observeParamDelivery 导出各实例的参数投递是否已连续多期没有送达：实例在运行、DSL 有可变成员（固化的解析里有可变成员的参数声明）、
// 并且当前会话最近 N 期带参数的结果都没有送达时为 1，否则为 0；不再启用的实例把已导出的序列清零。
func (o *instanceObserver) observeParamDelivery(ctx context.Context, instances []store.Instance) {
	if o.paramStale == nil {
		return
	}
	current := make(map[string]string, len(instances))
	for _, instance := range instances {
		resolved, err := input.ParseResolved(instance.ResolvedJSON)
		if err != nil || len(resolved.FactorParams) == 0 || instance.SessionID == nil {
			continue
		}
		stale := false
		if o.staleAlertPeriods > 0 {
			var err error
			if stale, err = o.store.ParamDeliveryStale(ctx, instance.InstanceID, *instance.SessionID, o.staleAlertPeriods); err != nil {
				o.log("读取实例 %s 的参数投递状态失败：%v", instance.InstanceID, err)
				continue
			}
		}
		current[instance.InstanceID] = instance.SpaceID
		value := 0.0
		if stale {
			value = 1
		}
		o.paramStale.WithLabelValues(instance.SpaceID, instance.InstanceID).Set(value)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, space := range o.staleExported {
		if _, still := current[id]; !still {
			o.paramStale.WithLabelValues(space, id).Set(0)
			delete(o.staleExported, id)
		}
	}
	for id, space := range current {
		o.staleExported[id] = space
	}
}

// ParamSubmitted 记录一条可变成员参数投递的终态（delivered、rejected）。
func (o *instanceObserver) ParamSubmitted(status string) {
	if o != nil && o.paramSubmits != nil {
		o.paramSubmits.WithLabelValues(status).Inc()
	}
}
