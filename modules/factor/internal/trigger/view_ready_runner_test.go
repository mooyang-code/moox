package trigger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testReady(period time.Time, status, failed string) *publicstoragepb.ViewDataReady {
	ready := &publicstoragepb.ViewDataReady{
		ViewId: "source_view", ViewConfigId: "source_view@1", CompletionEventId: "ready",
		CompletionKind: events.MergePeriodCompleted.Name(),
		DatasetId:      "prices", Status: status, VisibleScope: "view:source_view",
		Frequency: "1m", PeriodTime: period.Unix(),
		CommittedPositions: []*publicstoragepb.CommittedPosition{{NodeId: "n", StoreId: "s", Sequence: 1}},
		ReadyAt:            timestamppb.New(period),
	}
	if status == "degraded" {
		if failed == "" {
			failed = "degraded"
		}
		ready.FailedScopeRef = failed
	}
	return ready
}

func freezeClock(period time.Time) func() time.Time {
	return func() time.Time { return period.UTC().Add(30 * time.Second) }
}

func TestViewReadyRunnerRunsSubjectFactorCartesianProduct(t *testing.T) {
	runner := &blockingCombinationRunner{tasks: make(chan []taskrunner.Task, 1), release: make(chan struct{})}
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	ready := testReady(period, "complete", "")

	done := make(chan error, 1)
	go func() { done <- executor.Execute(context.Background(), "space", "source-event", ready) }()
	tasks := <-runner.tasks
	got := make([]string, 0, len(tasks))
	for _, task := range tasks {
		got = append(got, task.Factor.FactorID)
		require.Equal(t, taskrunner.DeterministicTaskID(task), task.TaskID)
		require.Empty(t, task.SubjectID)
		require.Equal(t, []string{"BTC", "ETH", "SOL"}, task.ExpectedSubjects)
		require.Equal(t, "complete", task.InputStatus)
		require.Zero(t, task.ExpectedActiveIndexRevision, "live View writes must not fence a view-ready read")
	}
	require.Equal(t, []string{"bias5", "bias20"}, got)
	require.Nil(t, storage.getMarker(), "marker must wait for every combination to become terminal")

	close(runner.release)
	require.NoError(t, <-done)
	require.Equal(t, "complete", storage.getMarker().GetStatus())
	require.Equal(t, 1, storage.markerCount())
}

func TestViewReadyRunnerExecuteSelectedRunsOnlyRequestedFactor(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 1, 1, 0, 0, time.UTC)
	ready := testReady(period, "complete", "")

	require.NoError(t, executor.ExecuteSelected(context.Background(), "space", "selected-event", "bias5", ready))
	require.Len(t, runner.tasks, 1)
	require.Equal(t, "bias5", runner.tasks[0].Factor.FactorID)
	require.Empty(t, runner.tasks[0].SubjectID)
	marker := storage.getMarker()
	require.Len(t, marker.GetBindings(), 1)
	require.Equal(t, "bias5", marker.GetBindings()[0].GetFactorId())
}

func TestViewReadyRunnerIgnoresTimeSeriesOnlyBindings(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindingsWithReadiness{
			periodBindings: periodBindings{rows: []domain.FactorBinding{{
				BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
				SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
				Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
			}}},
		},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
	)
	ready := testReady(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), "complete", "")
	ready.DatasetId = "prices"
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready", ready))
	require.Empty(t, runner.tasks)
	require.Zero(t, storage.markerCount())
	require.Empty(t, reports.markers)
}

func TestViewReadyRunnerFreezesTimeSeriesUniverseWithoutRunning(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(time.Date(2026, 9, 14, 10, 3, 0, 0, time.UTC))),
	)
	period := time.Date(2026, 9, 14, 10, 3, 0, 0, time.UTC)
	ready := testReady(period, "complete", "")
	ready.DatasetId = "prices"
	ready.UniverseSubjectIds = []string{"BTC-USDT", "ETH-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready", ready))
	require.Empty(t, runner.tasks)
	require.Zero(t, storage.markerCount())
	require.Empty(t, reports.markers)

	key := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: period.Unix()}
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Empty(t, reports.markers)
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "ETH-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "eth", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, reports.markers[0].GetUniverseSubjectIds())
	require.Equal(t, "setup-Bias", reports.markers[0].GetBindings()[0].GetBindingId())
	require.Equal(t, "Bias", reports.markers[0].GetBindings()[0].GetFactorId())
}

func TestPeriodSubjectUniversePrefersVisibleSubjectOverExpectedSet(t *testing.T) {
	ready := testReady(time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC), "complete", "")
	ready.UniverseSubjectIds = []string{"BTC-USDT", "ETH-USDT"}
	ready.VisibleScope = "subject:BTC-USDT"
	require.Equal(t, []string{"BTC-USDT"}, periodSubjectUniverse(nil, ready))
}

func TestViewReadyRunnerFreezeRetriesWhenMergedDatasetMissing(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	period := time.Date(2026, 9, 14, 10, 3, 0, 0, time.UTC)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithCatalogStore(db),
		WithNow(freezeClock(period)),
	)
	ready := testReady(period, "complete", "")
	ready.DatasetId = "prices"
	ready.UniverseSubjectIds = []string{"BTC-USDT"}
	require.Error(t, executor.Execute(context.Background(), "space", "merge-ready", ready))
	require.Empty(t, reports.markers)
}

func TestViewReadyRunnerKeepsMergeFailedTimeSeriesPendingUntilExpiry(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	period := time.Date(2026, 9, 15, 1, 39, 0, 0, time.UTC)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(period)),
	)
	ready := testReady(period, "degraded", "ETH-USDT")
	ready.DatasetId = "prices"
	ready.UniverseSubjectIds = []string{"BTC-USDT", "ETH-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-degraded", ready))
	require.Empty(t, runner.tasks)
	require.Empty(t, reports.markers)

	key := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: period.Unix()}
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Empty(t, reports.markers, "timeseries must keep merge-failed subjects pending for DatasetRowsUpserted")

	require.NoError(t, barrier.ExpirePending(context.Background(), period.Add(16*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "degraded", reports.markers[0].GetStatus())
	require.Equal(t, []string{"ETH-USDT"}, reports.markers[0].GetBindings()[0].GetSkippedSubjects())
}

func TestViewReadyRunnerDoesNotFreezeNextPeriodWhilePreviousIsOpen(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(time.Date(2026, 9, 14, 11, 23, 0, 0, time.UTC))),
	)
	first := time.Date(2026, 9, 14, 11, 23, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	firstReady := testReady(first, "complete", "")
	firstReady.DatasetId = "prices"
	firstReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-1", firstReady))

	secondReady := testReady(second, "complete", "")
	secondReady.DatasetId = "prices"
	secondReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-2", secondReady))

	secondKey := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: second.Unix()}
	require.NoError(t, barrier.Record(context.Background(), secondKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-2", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Empty(t, reports.markers, "a newer period must not freeze while an earlier ledger is still open")

	firstKey := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: first.Unix()}
	require.NoError(t, barrier.Record(context.Background(), firstKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-1", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, first.Unix(), reports.markers[0].GetPeriodTime())

	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-2-retry", secondReady))
	require.NoError(t, barrier.Record(context.Background(), secondKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-2", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Len(t, reports.markers, 2)
	require.Equal(t, second.Unix(), reports.markers[1].GetPeriodTime())
}

func TestViewReadyRunnerSkipsCatchUpFreezeWhenLaterLedgerExists(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(time.Date(2026, 9, 14, 16, 0, 0, 0, time.UTC))),
	)
	current := time.Date(2026, 9, 14, 16, 0, 0, 0, time.UTC)
	currentReady := testReady(current, "complete", "")
	currentReady.DatasetId = "prices"
	currentReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-current", currentReady))
	currentKey := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: current.Unix()}
	require.NoError(t, barrier.Record(context.Background(), currentKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-now", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Len(t, reports.markers, 1)

	stale := current.Add(-2 * time.Hour)
	staleReady := testReady(stale, "complete", "")
	staleReady.DatasetId = "prices"
	staleReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-stale", staleReady))

	staleKey := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: stale.Unix()}
	require.NoError(t, barrier.Record(context.Background(), staleKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-old", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Len(t, reports.markers, 1, "catch-up ViewDataReady must not freeze behind a later reported ledger")
	require.Equal(t, current.Unix(), reports.markers[0].GetPeriodTime())
}

func TestViewReadyRunnerSkipsStaleCatchUpFreezeWithoutLaterLedger(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	period := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(func() time.Time { return period.Add(20 * time.Minute) }),
	)
	ready := testReady(period, "complete", "")
	ready.DatasetId = "prices"
	ready.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-stale", ready))

	key := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: period.Unix()}
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-old", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Empty(t, reports.markers, "expired catch-up ViewDataReady must not freeze a period ledger")
}

func TestViewReadyRunnerFreezesLatestPeriodWhenEarlierLedgerIsStale(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{{
			BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
			SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
			Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
		}}},
		periodFactors{"Bias": testFactor("Bias")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(time.Date(2026, 9, 14, 11, 21, 0, 0, time.UTC))),
	)
	stale := time.Date(2026, 9, 14, 11, 16, 0, 0, time.UTC)
	staleReady := testReady(stale, "complete", "")
	staleReady.DatasetId = "prices"
	staleReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-stale", staleReady))

	current := stale.Add(5 * time.Minute)
	currentReady := testReady(current, "complete", "")
	currentReady.DatasetId = "prices"
	currentReady.UniverseSubjectIds = []string{"BTC-USDT"}
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready-current", currentReady))

	currentKey := PeriodKey{SpaceID: "space", DatasetID: "prices", SnapshotID: "-", Frequency: "1m", PeriodTime: current.Unix()}
	require.NoError(t, barrier.Record(context.Background(), currentKey, PairOutcome{
		BindingID: "setup-Bias", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc-now", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, current.Unix(), reports.markers[0].GetPeriodTime())
}

func TestViewReadyRunnerRetriesWhileCrossSectionBindingIsPending(t *testing.T) {
	executor := NewViewReadyRunner(
		periodBindingsWithReadiness{
			periodBindings: periodBindings{rows: []domain.FactorBinding{{
				BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
				SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
				Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
			}}},
			waiting: true,
		},
		periodFactors{"Bias": testFactor("Bias")},
		new(recordingCombinationRunner), new(periodStorageFake), t.TempDir(),
	)
	ready := testReady(time.Date(2026, 9, 14, 10, 1, 0, 0, time.UTC), "complete", "")
	require.ErrorIs(t, executor.Execute(context.Background(), "space", "merge-ready", ready), ErrBindingNotReady)
}

func TestViewReadyRunnerRunsCrossSectionAndIgnoresTimeSeries(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	storage := new(periodStorageFake)
	runner := new(recordingCombinationRunner)
	executor := NewViewReadyRunner(
		periodBindings{rows: []domain.FactorBinding{
			{
				BindingID: "setup-Bias", BindingGeneration: "incarnation-1", FactorID: "Bias",
				SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
				Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC"]`,
				Status: domain.BindingStatusEnabled,
			},
			{
				BindingID: "b-05-bias5", BindingGeneration: "incarnation-1", FactorID: "bias5",
				SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "prices",
				Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC"]`,
				Status: domain.BindingStatusEnabled,
			},
		}},
		periodFactors{"Bias": testFactor("Bias"), "bias5": testCrossSectionFactor("bias5")},
		runner, storage, t.TempDir(),
		WithPeriodBarrier(barrier),
		WithNow(freezeClock(time.Date(2026, 9, 14, 10, 2, 0, 0, time.UTC))),
	)
	ready := testReady(time.Date(2026, 9, 14, 10, 2, 0, 0, time.UTC), "complete", "")
	ready.DatasetId = "prices"
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-ready", ready))
	require.Len(t, runner.tasks, 1)
	require.Equal(t, "bias5", runner.tasks[0].Factor.FactorID)
	require.Zero(t, storage.markerCount())
	for _, marker := range reports.markers {
		require.Len(t, marker.GetBindings(), 1)
		require.Equal(t, "b-05-bias5", marker.GetBindings()[0].GetBindingId())
	}
}

func TestViewReadyRunnerExecutionBudgetIgnoresTimeSeriesBindings(t *testing.T) {
	bindings := []domain.FactorBinding{
		{BindingID: "ts", FactorID: "Bias", SpaceID: "space", SourceViewID: "source_view", Freq: "1m", Status: domain.BindingStatusEnabled},
		{BindingID: "cs", FactorID: "bias5", SpaceID: "space", SourceViewID: "source_view", Freq: "1m", Status: domain.BindingStatusEnabled},
	}
	runner := NewViewReadyRunner(periodBindings{rows: bindings}, periodFactors{
		"Bias": testFactor("Bias"), "bias5": testCrossSectionFactor("bias5"),
	}, nil, nil, "", WithExecutionUnitTimeout(30*time.Second))
	budget, err := runner.ExecutionBudget(context.Background(), "space", &publicstoragepb.ViewDataReady{
		ViewId: "source_view", Frequency: "1m", CompletionKind: events.MergePeriodCompleted.Name(),
	})
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute+30*time.Second, budget)
}

func TestViewReadyRunnerExecutionBudgetScalesWithBindingCount(t *testing.T) {
	bindings := make([]domain.FactorBinding, 31)
	for i := range bindings {
		bindings[i] = domain.FactorBinding{
			BindingID: fmt.Sprintf("binding-%d", i), FactorID: fmt.Sprintf("factor-%d", i),
			SpaceID: "space", SourceViewID: "source_view", Freq: "1m", Status: domain.BindingStatusEnabled,
		}
	}
	runner := NewViewReadyRunner(periodBindings{rows: bindings}, nil, nil, nil, "", WithExecutionUnitTimeout(30*time.Second))
	budget, err := runner.ExecutionBudget(context.Background(), "space", &publicstoragepb.ViewDataReady{
		ViewId: "source_view", Frequency: "1m", CompletionKind: events.MergePeriodCompleted.Name(),
	})
	require.NoError(t, err)
	require.Equal(t, 17*time.Minute+30*time.Second, budget)
}

func TestViewReadyRunnerExecutionBudgetScalesWithSubjectsAndWorkers(t *testing.T) {
	bindings := make([]domain.FactorBinding, 4)
	for i := range bindings {
		bindings[i] = domain.FactorBinding{
			BindingID: fmt.Sprintf("binding-%d", i), FactorID: fmt.Sprintf("factor-%d", i),
			SpaceID: "space", SourceViewID: "source_view", Freq: "1m", Status: domain.BindingStatusEnabled,
			SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["s1","s2","s3","s4","s5","s6","s7","s8","s9","s10"]`,
		}
	}
	runner := NewViewReadyRunner(periodBindings{rows: bindings}, nil, nil, nil, "",
		WithExecutionUnitTimeout(30*time.Second), WithExecutionParallelism(4), WithBatchExecution(true))
	budget, err := runner.ExecutionBudget(context.Background(), "space", &publicstoragepb.ViewDataReady{
		ViewId: "source_view", Frequency: "1m", CompletionKind: events.MergePeriodCompleted.Name(),
	})
	require.NoError(t, err)
	// Batch execution runs 3 subject waves. Each four-factor batch may legally
	// consume 4 * 30 seconds, plus the fixed margin.
	require.Equal(t, 8*time.Minute, budget)
}

func TestViewReadyRunnerIsolatesCombinationFailure(t *testing.T) {
	runner := &recordingCombinationRunner{fail: map[string]error{"b-20-bias20\x00": errors.New("python failed")}}
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	err := executor.Execute(context.Background(), "space", "source-event", testReady(period, "complete", ""))
	require.NoError(t, err)
	require.Len(t, runner.tasks, 2)
	require.Equal(t, []string{"b-20-bias20/"}, storage.clearedCombinations())
	marker := storage.getMarker()
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, 1, storage.markerCount())
	require.Equal(t, "b-05-bias5", marker.GetBindings()[0].GetBindingId())
	require.Equal(t, "complete", marker.GetBindings()[0].GetStatus())
	require.Equal(t, "b-20-bias20", marker.GetBindings()[1].GetBindingId())
	require.Equal(t, "degraded", marker.GetBindings()[1].GetStatus())
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, marker.GetBindings()[1].GetFailedSubjects())
}

func TestViewReadyRunnerClearsSkippedAndUpstreamFailedWithoutRunningThem(t *testing.T) {
	bindings := periodBindings{rows: []domain.FactorBinding{{
		BindingID: "binding", BindingGeneration: "incarnation-1", FactorID: "factor", SpaceID: "space", SourceViewID: "source_view",
		ResultDatasetID: "result", Freq: "1m", SubjectMode: domain.SubjectModeInclude,
		SubjectsJSON: `["BTC"]`, Status: domain.BindingStatusEnabled,
	}}}
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	period := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)
	err := NewViewReadyRunner(bindings, periodFactors{"factor": testCrossSectionFactor("factor")}, runner, storage, t.TempDir()).Execute(
		context.Background(), "space", "source-event", testReady(period, "degraded", "BTC"))
	require.NoError(t, err)
	require.Empty(t, runner.tasks)
	require.Equal(t, []string{"binding/BTC"}, storage.clearedCombinations())
	state := storage.getMarker().GetBindings()[0]
	require.Equal(t, []string{"BTC"}, state.GetSkippedSubjects())
	require.Empty(t, state.GetFailedSubjects())
}

func TestViewReadyRunnerMarksBindingDegradedForAuxiliaryFailure(t *testing.T) {
	bindings := periodBindings{rows: []domain.FactorBinding{{BindingID: "binding", BindingGeneration: "incarnation-1", FactorID: "factor", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", Status: domain.BindingStatusEnabled}}}
	storage := new(periodStorageFake)
	period := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	err := NewViewReadyRunner(bindings, periodFactors{"factor": testCrossSectionFactor("factor")}, new(recordingCombinationRunner), storage, t.TempDir()).Execute(context.Background(), "space", "source-event", testReady(period, "degraded", "OTHER"))
	require.NoError(t, err)
	require.Equal(t, "degraded", storage.getMarker().GetStatus())
	require.Equal(t, "degraded", storage.getMarker().GetBindings()[0].GetStatus())
}

func TestViewReadyRunnerRealtimeNoBindingIsNoop(t *testing.T) {
	storage := &periodStorageFake{preflightErr: errors.New("result dataset is not found")}
	executor := NewViewReadyRunner(periodBindings{}, periodFactors{}, new(recordingCombinationRunner), storage, t.TempDir())
	ready := testReady(time.Date(2026, 8, 10, 5, 0, 0, 0, time.UTC), "complete", "")
	require.NoError(t, executor.Execute(context.Background(), "space", "source-event", ready))
	require.Nil(t, storage.getMarker())
	require.ErrorIs(t, executor.ExecuteSelected(context.Background(), "space", "recalc-event", "", ready), ErrNoExecutableBinding)
}

func TestViewReadyRunnerRetriesWhileBindingIsPending(t *testing.T) {
	bindings := pendingPeriodBindings{waiting: true}
	executor := NewViewReadyRunner(bindings, periodFactors{}, new(recordingCombinationRunner), new(periodStorageFake), t.TempDir())
	ready := testReady(time.Date(2026, 8, 10, 5, 1, 0, 0, time.UTC), "complete", "")
	require.ErrorIs(t, executor.Execute(context.Background(), "space", "source-event", ready), ErrBindingNotReady)
}

func twoPeriodBindings() periodBindings {
	return periodBindings{rows: []domain.FactorBinding{
		{BindingID: "b-20-bias20", BindingGeneration: "incarnation-1", FactorID: "bias20", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC","ETH","SOL"]`, Status: domain.BindingStatusEnabled},
		{BindingID: "b-05-bias5", BindingGeneration: "incarnation-1", FactorID: "bias5", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC","ETH","SOL"]`, Status: domain.BindingStatusEnabled},
	}}
}

func twoPeriodFactors() periodFactors {
	return periodFactors{"bias5": testCrossSectionFactor("bias5"), "bias20": testCrossSectionFactor("bias20")}
}

func testFactor(id string) domain.FactorDef {
	return domain.FactorDef{FactorType: domain.FactorTypeTimeSeries, FactorID: id, Name: id, SourceHash: "hash-" + id, InputColumns: []string{"close"}, Outputs: []string{id}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled}
}

func testCrossSectionFactor(id string) domain.FactorDef {
	return domain.FactorDef{FactorType: domain.FactorTypeCrossSection, FactorID: id, Name: id, SourceHash: "hash-" + id, InputColumns: []string{"close"}, Outputs: []string{id}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled}
}

type periodBindings struct{ rows []domain.FactorBinding }

func (p periodBindings) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return append([]domain.FactorBinding(nil), p.rows...), nil
}

type periodBindingsWithReadiness struct {
	periodBindings
	waiting bool
}

func (p periodBindingsWithReadiness) HasExecutableOrPending(context.Context, string, string, string) (bool, error) {
	return p.waiting, nil
}

type pendingPeriodBindings struct{ waiting bool }

func (p pendingPeriodBindings) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return nil, nil
}

func (p pendingPeriodBindings) HasExecutableOrPending(context.Context, string, string, string) (bool, error) {
	return p.waiting, nil
}

type periodFactors map[string]domain.FactorDef

func (p periodFactors) Get(_ context.Context, factorID string) (*domain.FactorDef, error) {
	factor, ok := p[factorID]
	if !ok {
		return nil, fmt.Errorf("factor %s not found", factorID)
	}
	value := factor
	return &value, nil
}

type blockingCombinationRunner struct {
	tasks   chan []taskrunner.Task
	release chan struct{}
}

func (r *blockingCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	copied := append([]taskrunner.Task(nil), tasks...)
	r.tasks <- copied
	<-r.release
	results := make([]taskrunner.Result, len(copied))
	for index := range copied {
		results[index] = taskrunner.Result{Task: copied[index], Write: testFactorWrite()}
	}
	return results
}

type recordingCombinationRunner struct {
	mu    sync.Mutex
	tasks []taskrunner.Task
	fail  map[string]error
}

func (r *recordingCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	r.mu.Lock()
	r.tasks = append(r.tasks, tasks...)
	r.mu.Unlock()
	results := make([]taskrunner.Result, len(tasks))
	for index, task := range tasks {
		result := taskrunner.Result{Task: task, Err: r.fail[combinationKey(task.BindingID, task.SubjectID)]}
		if result.Err == nil {
			result.Write = testFactorWrite()
		}
		results[index] = result
	}
	return results
}

func testFactorWrite() storageio.FactorWrite {
	return storageio.FactorWrite{Rows: 1, Receipts: []storageio.WriteReceipt{{
		CommitID: "test-patch", NodeID: "n", StoreID: "s", Sequence: 1,
	}}}
}

type periodStorageFake struct {
	mu           sync.Mutex
	marker       *storagepb.FactorPeriodComputedMarker
	markers      int
	cleared      []*engine.FactorTask
	preflightErr error
}

func (p *periodStorageFake) FactorPeriodComputed(context.Context, string, string, string, int64) (bool, error) {
	return false, p.preflightErr
}

func (p *periodStorageFake) ReportFactorPeriodComputed(_ context.Context, _ string, marker *storagepb.FactorPeriodComputedMarker) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.marker = marker
	p.markers++
	return nil
}

func (p *periodStorageFake) ClearFactorOutputs(_ context.Context, task *engine.FactorTask) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	copyTask := *task
	p.cleared = append(p.cleared, &copyTask)
	return nil
}

func (p *periodStorageFake) getMarker() *storagepb.FactorPeriodComputedMarker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.marker
}

func (p *periodStorageFake) markerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.markers
}

func (p *periodStorageFake) clearedCombinations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, 0, len(p.cleared))
	for _, task := range p.cleared {
		result = append(result, task.BindingID+"/"+task.SubjectID)
	}
	return result
}
