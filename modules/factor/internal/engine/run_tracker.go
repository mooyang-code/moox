package engine

import (
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

// runTracker keeps the latest live period per factor set in memory. The period
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

func (t *runTracker) record(setID string, periodTime time.Time, status string, outcome pipeline.Outcome, completedAt time.Time) {
	if t == nil || setID == "" || periodTime.IsZero() {
		return
	}
	t.mu.Lock()
	if current, ok := t.runs[setID]; ok && current.periodTime.After(periodTime) {
		t.mu.Unlock()
		return
	}
	factors := make([]domain.FactorPeriodState, 0, len(outcome.Factors))
	for _, factor := range outcome.Factors {
		factors = append(factors, domain.FactorPeriodState{
			FactorID:       factor.FactorID,
			Status:         factor.Status,
			FailedSubjects: factor.FailedSubjects,
			SourceHash:     factor.SourceHash,
			DefinitionHash: factor.DefinitionHash,
			GapSubjects:    factor.GapSubjects,
		})
	}
	t.runs[setID] = trackedRun{
		periodTime: periodTime, completedAt: completedAt, status: status, factors: factors,
		failedSubjects: outcome.FailedSubjects, warming: outcome.Warming,
	}
	t.mu.Unlock()
	t.notifyRecorded()
}

func (t *runTracker) all(now time.Time) []domain.SetRunSummary {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]domain.SetRunSummary, 0, len(t.runs))
	for setID, run := range t.runs {
		out = append(out, summaryOf(setID, run, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SetID < out[j].SetID })
	return out
}

func summaryOf(setID string, run trackedRun, now time.Time) domain.SetRunSummary {
	lag := int64(now.Sub(run.periodTime).Seconds())
	if lag < 0 {
		lag = 0
	}
	var completedAt int64
	if !run.completedAt.IsZero() {
		completedAt = run.completedAt.Unix()
	}
	return domain.SetRunSummary{
		SetID: setID, LastPeriodTime: run.periodTime.Unix(), CompletedAt: completedAt, LastStatus: run.status, LagSeconds: lag,
		Factors: run.factors, FailedSubjects: run.failedSubjects, WarmingSubjects: int32(run.warming),
	}
}
