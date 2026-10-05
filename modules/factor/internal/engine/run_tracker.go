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
}

type trackedRun struct {
	periodTime     time.Time
	status         string
	factors        []domain.FactorPeriodState
	failedSubjects []string
}

func newRunTracker() *runTracker { return &runTracker{runs: make(map[string]trackedRun)} }

func (t *runTracker) record(setID string, periodTime time.Time, status string, outcome pipeline.Outcome) {
	if t == nil || setID == "" || periodTime.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if current, ok := t.runs[setID]; ok && current.periodTime.After(periodTime) {
		return
	}
	factors := make([]domain.FactorPeriodState, 0, len(outcome.Factors))
	for _, factor := range outcome.Factors {
		factors = append(factors, domain.FactorPeriodState{
			FactorID:       factor.FactorID,
			Status:         factor.Status,
			FailedSubjects: factor.FailedSubjects,
			SourceHash:     factor.SourceHash,
		})
	}
	t.runs[setID] = trackedRun{
		periodTime: periodTime, status: status, factors: factors,
		failedSubjects: outcome.FailedSubjects,
	}
}

func (t *runTracker) latest(setID string, now time.Time) domain.SetRunSummary {
	if t == nil {
		return domain.SetRunSummary{SetID: setID}
	}
	t.mu.RLock()
	run, ok := t.runs[setID]
	t.mu.RUnlock()
	if !ok {
		return domain.SetRunSummary{SetID: setID}
	}
	return summaryOf(setID, run, now)
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
	return domain.SetRunSummary{
		SetID: setID, LastPeriodTime: run.periodTime.Unix(), LastStatus: run.status, LagSeconds: lag,
		Factors: run.factors, FailedSubjects: run.failedSubjects,
	}
}
