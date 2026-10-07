package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/report"
)

// engineView is the part of the engine hub the observer reads: the catalog the
// engine computes and the engine's latest heartbeat.
type engineView interface {
	Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineSet, error)
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
	// observed is the latest period recorded per set, so a heartbeat repeating
	// the same period is not counted as another run. Timer ticks may overlap.
	mu       sync.Mutex
	observed map[string]int64
}

func newFactorDatasetObserver(engine engineView, metrics *report.DatasetMetrics) *factorDatasetObserver {
	return &factorDatasetObserver{engine: engine, metrics: metrics, clock: periodclock.Continuous{}, now: time.Now, observed: make(map[string]int64)}
}

func (o *factorDatasetObserver) observe(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _, sets, err := o.engine.Snapshot(ctx, "")
	if err != nil {
		o.metrics.ObserveInventoryRefreshError()
		return fmt.Errorf("load factor sets: %w", err)
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
		keys[set.Set.SetID] = key
		expected = append(expected, report.DatasetExpectation{Key: key, Interval: interval})
	}
	if err := o.metrics.ReplaceExpected(expected); err != nil {
		return fmt.Errorf("replace expected factor datasets: %w", err)
	}
	_, status, ok, err := o.engine.Engine(ctx)
	if err != nil || !ok {
		return err
	}
	finishedAt := o.now().UTC()
	for _, run := range status.RecentRuns {
		key, expected := keys[run.SetID]
		if !expected || run.LastPeriodTime <= o.observed[run.SetID] {
			continue
		}
		o.observed[run.SetID] = run.LastPeriodTime
		observation := report.DatasetObservation{Key: key, Result: datasetResult(run), FinishedAt: finishedAt}
		// Warming results are not formal, so they do not advance the output.
		if run.WarmingSubjects == 0 && observation.Result != "error" {
			observation.OutputWatermark = time.Unix(run.LastPeriodTime, 0).UTC()
		}
		if err := o.metrics.ObserveRun(observation); err != nil {
			return fmt.Errorf("observe factor set %s: %w", run.SetID, err)
		}
	}
	return nil
}

func datasetResult(run domain.SetRunSummary) string {
	switch {
	case run.LastStatus == "failed":
		return "error"
	case run.LastStatus == "complete" && run.WarmingSubjects == 0:
		return "success"
	default:
		return "incomplete"
	}
}
