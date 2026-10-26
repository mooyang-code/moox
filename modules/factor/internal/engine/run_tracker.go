package engine

import (
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
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
	periodTime time.Time
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

func (t *runTracker) record(taskID string, periodTime time.Time, status string, outcome pipeline.Outcome, completedAt time.Time) {
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
			MemberKey:      factor.MemberKey,
			MemberKind:     factor.MemberKind,
			FactorID:       factor.FactorID,
			Status:         factor.Status,
			FailedSubjects: factor.FailedSubjects,
			SpecHash:       factor.SourceHash,
			DefinitionHash: factor.DefinitionHash,
			GapSubjects:    factor.GapSubjects,
		})
	}
	t.runs[taskID] = trackedRun{
		periodTime: periodTime, completedAt: completedAt, status: status, factors: factors,
		failedSubjects: outcome.FailedSubjects, warming: outcome.Warming, rows: outcome.RowsWritten,
	}
	t.mu.Unlock()
	t.notifyRecorded()
}

// repair 记下缺口补算对 periodTime 这一期补齐的 (因子, 标的)。只处理最新一期：更早的期次已被更新的结果取代。
// done 为真表示账本里这一期已经没有待恢复项，这时按补齐的结果改写摘要：去掉已补齐的失败标的与空洞标的，
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
		next.GapSubjects = subjectsWithout(factor.GapSubjects, fixed)
		// 整因子跳过的因子实时路径一行没写：补算重算时没写成的标的仍然缺结果。
		if factor.Status == "skipped" {
			next.FailedSubjects = mergeSubjects(next.FailedSubjects, run.unwritten[factor.MemberKey], fixed)
		}
		// 只因标的失败（含预热中、空洞）而不完整的因子，这些标的都补齐后记为完成；没有列出标的的整因子失败保持原状。
		if next.Status != "complete" && len(factor.FailedSubjects)+len(factor.GapSubjects) > 0 && len(next.FailedSubjects)+len(next.GapSubjects) == 0 {
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

func summaryOf(taskID string, run trackedRun, now time.Time) domain.TaskRunSummary {
	lag := int64(now.Sub(run.periodTime).Seconds())
	if lag < 0 {
		lag = 0
	}
	var completedAt int64
	if !run.completedAt.IsZero() {
		completedAt = run.completedAt.Unix()
	}
	return domain.TaskRunSummary{
		TaskID: taskID, LastPeriodTime: run.periodTime.Unix(), CompletedAt: completedAt, LastStatus: run.status, LagSeconds: lag,
		Factors: run.factors, FailedSubjects: run.failedSubjects, WarmingSubjects: int32(run.warming),
	}
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
