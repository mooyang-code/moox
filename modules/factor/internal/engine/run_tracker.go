package engine

import (
	"sort"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// runTracker keeps the latest live period per factor task in memory. The period
// pipeline has no durable ledger by design, so these values are only meant for
// status display and reset on restart.
type runTracker struct {
	mu   sync.RWMutex
	runs map[string]trackedRun
	// onRecord 在每记录一期后调用（锁外），引擎用它立刻补发心跳。
	onRecord func()
}

type trackedRun struct {
	// periodTime 是轴上的周期；axis 是任务所在空间、频率的换算，上报时换回真实时间。
	periodTime time.Time
	axis       periodclock.Axis
	// completedAt 是这一期结果写完的时刻，作为因子结果的到达时间上报给 Monitor。
	completedAt    time.Time
	status         string
	factors        []domain.FactorPeriodState
	failedSubjects []string
	warming        int
	// rows 是这一期写入的结果行数。
	rows int
	// repaired 是缺口补算已补齐的这一期 (因子 → 标的)：预热中或回看窗口有空洞而跳过的项补齐后，摘要据此改写。
	// unwritten 是补算重算这一期时没有写成的 (因子 → 标的)，整因子跳过的因子据此保留失败。
	repaired, unwritten map[string]map[string]struct{}
}

func newRunTracker() *runTracker { return &runTracker{runs: make(map[string]trackedRun)} }

// notifyRecorded 在一期记录下来之后调用（引擎用它唤醒心跳），在锁外执行。
func (t *runTracker) notifyRecorded() {
	t.mu.RLock()
	hook := t.onRecord
	t.mu.RUnlock()
	if hook != nil {
		hook()
	}
}

// setOnRecord 登记每记录一期就调用的函数。
func (t *runTracker) setOnRecord(hook func()) {
	t.mu.Lock()
	t.onRecord = hook
	t.mu.Unlock()
}

func (t *runTracker) record(taskID string, axis periodclock.Axis, periodTime time.Time, status string, outcome pipeline.Outcome, completedAt time.Time) {
	if t == nil || taskID == "" || periodTime.IsZero() {
		return
	}
	t.mu.Lock()
	if current, ok := t.runs[taskID]; ok && current.periodTime.After(periodTime) {
		t.mu.Unlock()
		return
	}
	factors := make([]domain.FactorPeriodState, 0, len(outcome.Factors))
	for _, factor := range outcome.Factors {
		factors = append(factors, domain.FactorPeriodState{
			MemberKey:         factor.MemberKey,
			MemberKind:        factor.MemberKind,
			FactorID:          factor.FactorID,
			Status:            factor.Status,
			FailedSubjects:    factor.FailedSubjects,
			SpecHash:          factor.SourceHash,
			DefinitionHash:    factor.DefinitionHash,
			FailedSeries:      factor.FailedSeries,
			SkippedSeriesTags: factor.SkippedSeriesTags,
		})
	}
	t.runs[taskID] = trackedRun{
		periodTime: periodTime, axis: axis, completedAt: completedAt, status: status, factors: factors,
		failedSubjects: outcome.FailedSubjects, warming: outcome.Warming, rows: outcome.RowsWritten,
	}
	t.mu.Unlock()
	t.notifyRecorded()
}

// repair 记下缺口补算对 periodTime 这一期补齐的 (因子, 标的)。只处理最新一期：更早的期次已被更新的结果取代。
// done 为真表示账本里这一期已经没有待恢复项，这时按补齐的结果改写摘要：去掉已补齐的失败标的，
// 不再算作预热中，余下没有失败时记为完成；completedAt 作为这一期新的完成时刻，并像实时周期一样立刻补发心跳。
// 已完成、已失败的期次不改写（失败的期次没有写完，补算只补跳过的项）。返回是否改写了摘要。
func (t *runTracker) repair(taskID string, result RepairedPeriod, completedAt time.Time) bool {
	if t == nil || taskID == "" {
		return false
	}
	t.mu.Lock()
	run, ok := t.runs[taskID]
	if !ok || !run.periodTime.Equal(result.Period) || run.status == "failed" || (run.status == "complete" && run.warming == 0) {
		t.mu.Unlock()
		return false
	}
	run.repaired = addItems(run.repaired, result.Repaired, result.Period)
	run.unwritten = addItems(run.unwritten, result.Unwritten, result.Period)
	if result.Done {
		run = run.withRepairs(completedAt)
	}
	t.runs[taskID] = run
	t.mu.Unlock()
	if result.Done {
		t.notifyRecorded()
	}
	return result.Done
}

// addItems 把属于 period 的 (因子, 标的) 并入 into。
func addItems(into map[string]map[string]struct{}, items []pipeline.ItemKey, period time.Time) map[string]map[string]struct{} {
	for _, item := range items {
		if !item.Period.Equal(period) {
			continue
		}
		if into == nil {
			into = make(map[string]map[string]struct{})
		}
		if into[item.Member] == nil {
			into[item.Member] = make(map[string]struct{})
		}
		into[item.Member][item.Subject] = struct{}{}
	}
	return into
}

// withRepairs 按补齐的 (因子, 标的) 改写一期的结果。补算没有覆盖的失败（上游失败、读取失败、计算出错、放弃或取消的待办）原样保留。
func (run trackedRun) withRepairs(completedAt time.Time) trackedRun {
	fixedAny := make(map[string]struct{})
	for _, subjects := range run.repaired {
		for subject := range subjects {
			fixedAny[subject] = struct{}{}
		}
	}
	stillFailed := make(map[string]struct{})
	complete := true
	factors := make([]domain.FactorPeriodState, len(run.factors))
	for i, factor := range run.factors {
		fixed := run.repaired[factor.MemberKey]
		next := factor
		next.FailedSubjects = subjectsWithout(factor.FailedSubjects, fixed)
		// 统一数据集：补齐的标的（它的各条序列都写成了）从按序列的名单里一并去掉。
		next.FailedSeries = seriesWithout(factor.FailedSeries, fixed)
		// 整因子跳过（含只跳过一部分 series_tag）的因子实时路径没写被跳过的行：补算重算时没写成的标的仍然缺结果。
		if factor.Status == "skipped" || len(factor.SkippedSeriesTags) > 0 {
			next.FailedSubjects = mergeSubjects(next.FailedSubjects, run.unwritten[factor.MemberKey], fixed)
		}
		// 跳过的 series_tag 要等该成员没有失败的标的了才算补齐。
		if len(next.FailedSubjects) == 0 {
			next.SkippedSeriesTags = nil
		}
		// 只因标的失败（含预热中）而不完整的因子，这些标的都补齐后记为完成；没有列出标的的整因子失败保持原状。
		if next.Status != "complete" && len(factor.FailedSubjects) > 0 && len(next.FailedSubjects) == 0 {
			next.Status = "complete"
		}
		for _, subject := range next.FailedSubjects {
			stillFailed[subject] = struct{}{}
		}
		if next.Status != "complete" {
			complete = false
		}
		factors[i] = next
	}
	var failed []string
	for _, subject := range run.failedSubjects {
		_, fixed := fixedAny[subject]
		_, still := stillFailed[subject]
		if fixed && !still {
			continue
		}
		failed = append(failed, subject)
	}
	run.factors, run.failedSubjects, run.warming, run.completedAt = factors, failed, 0, completedAt
	if complete && len(failed) == 0 {
		run.status = "complete"
	}
	return run
}

// mergeSubjects 把 extra 里不在 skip 中的标的并入 subjects，结果排序、去重。
func mergeSubjects(subjects []string, extra, skip map[string]struct{}) []string {
	if len(extra) == 0 {
		return subjects
	}
	seen := make(map[string]struct{}, len(subjects)+len(extra))
	out := make([]string, 0, len(subjects)+len(extra))
	for _, subject := range subjects {
		seen[subject] = struct{}{}
		out = append(out, subject)
	}
	for subject := range extra {
		if _, fixed := skip[subject]; fixed {
			continue
		}
		if _, ok := seen[subject]; !ok {
			seen[subject] = struct{}{}
			out = append(out, subject)
		}
	}
	sort.Strings(out)
	return out
}

// seriesWithout 去掉属于 drop 里标的的序列。
func seriesWithout(values []seriesset.Series, drop map[string]struct{}) []seriesset.Series {
	if len(drop) == 0 {
		return values
	}
	var out []seriesset.Series
	for _, value := range values {
		if _, ok := drop[value.SubjectID]; !ok {
			out = append(out, value)
		}
	}
	return out
}

func subjectsWithout(subjects []string, drop map[string]struct{}) []string {
	if len(drop) == 0 {
		return subjects
	}
	var out []string
	for _, subject := range subjects {
		if _, ok := drop[subject]; !ok {
			out = append(out, subject)
		}
	}
	return out
}

func (t *runTracker) all(now time.Time) []domain.TaskRunSummary {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]domain.TaskRunSummary, 0, len(t.runs))
	for taskID, run := range t.runs {
		out = append(out, summaryOf(taskID, run, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// summaryOf 是一期的对外摘要（心跳与 RecentRuns）：周期换回真实时间，滞后按交易 K 线根数计算，午休、隔夜、周末不算滞后。
func summaryOf(taskID string, run trackedRun, now time.Time) domain.TaskRunSummary {
	lastPeriod, err := run.axis.FromAxis(run.periodTime)
	if err != nil {
		lagErrors.report(taskID, err)
		return domain.TaskRunSummary{TaskID: taskID, LastStatus: run.status}
	}
	lag, err := lagSeconds(run.axis, run.periodTime, now)
	if err != nil {
		// 算不出交易 K 线根数（例如当前时刻超出交易日历的覆盖范围）：不能显示成“已追平”，记错误日志，
		// 滞后按真实时间流逝估算（不扣休市），让故障在页面上显示为滞后。
		lagErrors.report(taskID, err)
		lag = max(0, int64(now.Sub(lastPeriod)/time.Second))
	}
	var completedAt int64
	if !run.completedAt.IsZero() {
		completedAt = run.completedAt.Unix()
	}
	return domain.TaskRunSummary{
		TaskID: taskID, LastPeriodTime: lastPeriod.Unix(), CompletedAt: completedAt, LastStatus: run.status, LagSeconds: lag,
		Factors: run.factors, FailedSubjects: run.failedSubjects, WarmingSubjects: int32(run.warming),
	}
}

// lagBehind 是落后的根数：(AxisEnd(now) − 最后一期) / 周期 − 1。算完最新已收盘的一期为 0，收盘后、结果写出前为 1。
func lagBehind(axis periodclock.Axis, period, now time.Time) (int64, error) {
	end, err := axis.AxisEnd(now)
	if err != nil {
		return 0, err
	}
	return max(0, int64(end.Sub(period)/axis.Step())-1), nil
}

// lagErrorLog 限制滞后算不出来时的错误日志：每个任务每 10 分钟至多一条（心跳每几秒就算一次摘要）。
type lagErrorLog struct {
	mu     sync.Mutex
	logged map[string]time.Time
}

var lagErrors = &lagErrorLog{logged: map[string]time.Time{}}

func (l *lagErrorLog) report(taskID string, err error) {
	l.mu.Lock()
	last, seen := l.logged[taskID]
	now := time.Now()
	if seen && now.Sub(last) < 10*time.Minute {
		l.mu.Unlock()
		return
	}
	l.logged[taskID] = now
	l.mu.Unlock()
	log.Errorf("factor_lag_unknown task_id=%s error=%v（按交易 K 线计算滞后失败，可能是交易日历过期）", taskID, err)
}

// closeGrace 是收盘后给计算留的宽限：不超过半个周期，刚收盘、正在计算的那根在宽限内不算落后。
const closeGrace = 30 * time.Second

// lagSeconds 是落后的已收盘 K 线根数 × 周期。根数按市场时钟（交易序号轴）数，午休、隔夜、周末不算落后；
// 前端直接按 lag_seconds / 周期 显示根数。
func lagSeconds(axis periodclock.Axis, period, now time.Time) (int64, error) {
	step := axis.Step()
	bars, err := lagBehind(axis, period, now.Add(-min(closeGrace, step/2)))
	return bars * int64(step/time.Second), err
}

// lastRecorded 返回任务最近记录的一期原始记录（只在内存里，引擎重启后清空）。
func (t *runTracker) lastRecorded(taskID string) (trackedRun, bool) {
	if t == nil {
		return trackedRun{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	run, ok := t.runs[taskID]
	return run, ok
}
