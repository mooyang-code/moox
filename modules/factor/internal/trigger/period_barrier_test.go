package trigger

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

func TestFactorPeriodBarrierAggregatesEarlyTasksAfterFreeze(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC))
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "bias5", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Empty(t, reports.markers, "early tasks must wait for the frozen universe")

	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT", "ETH-USDT"},
		Bindings: []FrozenBinding{testFrozenBinding("bias5", "bias5", domain.FactorTypeTimeSeries, []string{"BTC-USDT", "ETH-USDT"})},
		BatchID:  "merge-1", ScopeRef: "scope:kline",
	}))
	require.Empty(t, reports.markers, "ETH is still unaccounted")

	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "bias5", SubjectID: "ETH-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "eth", NodeID: "n", StoreID: "s", Sequence: 2},
	}))
	require.Len(t, reports.markers, 1)
	marker := reports.markers[0]
	require.Equal(t, "complete", marker.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetExpectedSubjectIds())
	require.Equal(t, "merge-1", marker.GetBatchId())
	require.Len(t, marker.GetBindings(), 1)
	require.Equal(t, "complete", marker.GetBindings()[0].GetStatus())
}

func TestFactorPeriodBarrierIgnoresBindingChangeAfterFreeze(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 1, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT"},
		Bindings: []FrozenBinding{testFrozenBinding("old", "old-factor", domain.FactorTypeTimeSeries, []string{"BTC-USDT"})},
		BatchID:  "merge-old",
	}))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT"},
		Bindings: []FrozenBinding{testFrozenBinding("new", "new-factor", domain.FactorTypeTimeSeries, []string{"BTC-USDT"})},
		BatchID:  "merge-new",
	}))
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "new", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "new", NodeID: "n", StoreID: "s", Sequence: 9},
	}))
	require.Empty(t, reports.markers, "new bindings must not join a frozen period")

	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "old", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "old", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "old", reports.markers[0].GetBindings()[0].GetBindingId())
}

func TestFactorPeriodBarrierDoesNotWaitForeverForMissingSource(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 2, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key:              key,
		ExpectedSubjects: []string{"BTC-USDT", "ETH-USDT"},
		FailedSubjects:   []string{"ETH-USDT"},
		Bindings:         []FrozenBinding{testFrozenBinding("bias5", "bias5", domain.FactorTypeTimeSeries, []string{"BTC-USDT", "ETH-USDT"})},
		BatchID:          "merge-degraded",
	}))
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "bias5", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "btc", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Len(t, reports.markers, 1)
	marker := reports.markers[0]
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetExpectedSubjectIds())
	require.Equal(t, []string{"ETH-USDT"}, marker.GetBindings()[0].GetSkippedSubjects())
}

func TestFactorPeriodBarrierDoesNotPublishWithoutOutputReceipt(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 3, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT"},
		Bindings: []FrozenBinding{testFrozenBinding("bias5", "bias5", domain.FactorTypeTimeSeries, []string{"BTC-USDT"})},
		BatchID:  "merge-receipt",
	}))
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "bias5", SubjectID: "BTC-USDT", Status: PairComplete,
	}))
	require.Empty(t, reports.markers, "successful tasks without output receipts must not report complete")

	require.NoError(t, barrier.ConfirmReceipt(context.Background(), key, "bias5", "BTC-USDT", WriteReceipt{
		CommitID: "btc", NodeID: "n", StoreID: "s", Sequence: 4,
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].GetStatus())
	require.EqualValues(t, 4, reports.markers[0].GetCommittedPositions()[0].GetSequence())
}

func TestFactorPeriodBarrierZeroBindingsPublishesTerminal(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 4, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT"}, Bindings: nil, BatchID: "merge-empty",
	}))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "complete", reports.markers[0].GetStatus())
	require.Empty(t, reports.markers[0].GetBindings())
	require.Equal(t, []string{"BTC-USDT"}, reports.markers[0].GetExpectedSubjectIds())
}

func TestFactorPeriodBarrierIndependentViewsShareOneDatasetCompletion(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	key := testBarrierKey(time.Date(2026, 9, 13, 10, 5, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: key, ExpectedSubjects: []string{"BTC-USDT"},
		Bindings: []FrozenBinding{testFrozenBinding("rank", "rank", domain.FactorTypeCrossSection, []string{"BTC-USDT"})},
		BatchID:  "merge-views",
	}))
	require.NoError(t, barrier.Record(context.Background(), key, PairOutcome{
		BindingID: "rank", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "rank", NodeID: "n", StoreID: "s", Sequence: 8},
	}))
	require.Len(t, reports.markers, 1, "View fan-out belongs to Storage; the ledger emits one dataset completion")
	require.Equal(t, key.DatasetID, reports.markers[0].GetDatasetId())
	require.NoError(t, barrier.CloseIfReady(context.Background(), key))
	require.Len(t, reports.markers, 1, "retry must stay idempotent")
}

func TestFactorPeriodBarrierPruneKeepsReplayWindow(t *testing.T) {
	barrier, reports := openPeriodBarrier(t)
	first := testBarrierKey(time.Date(2026, 9, 13, 10, 6, 0, 0, time.UTC))
	second := testBarrierKey(time.Date(2026, 9, 13, 10, 7, 0, 0, time.UTC))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: first, ExpectedSubjects: []string{"BTC-USDT"}, Bindings: nil, BatchID: "old",
	}))
	require.NoError(t, barrier.Freeze(context.Background(), FreezeSpec{
		Key: second, ExpectedSubjects: []string{"BTC-USDT"}, Bindings: nil, BatchID: "new",
	}))
	require.Len(t, reports.markers, 2)
	require.ErrorContains(t, barrier.PruneClosedBefore(context.Background(), second.PeriodTime), "replay")
	require.NoError(t, barrier.PruneClosedBefore(context.Background(), first.PeriodTime))
	require.NoError(t, barrier.Record(context.Background(), first, PairOutcome{
		BindingID: "gone", SubjectID: "BTC-USDT", Status: PairComplete,
		Receipt: WriteReceipt{CommitID: "late", NodeID: "n", StoreID: "s", Sequence: 1},
	}))
	require.Len(t, reports.markers, 2, "pruned periods must not resurrect completions")
}

func TestFactorPeriodBarrierStrategyWaitsForResultViewReady(t *testing.T) {
	require.False(t, AcceptsStrategyViewReady(events.MergePeriodCompleted.Name(), true), "input ViewDataReady must not run factor-backed strategies")
	require.False(t, AcceptsStrategyViewReady(events.CollectorPeriodCompleted.Name(), true))
	require.True(t, AcceptsStrategyViewReady(events.FactorPeriodComputed.Name(), true))
	require.True(t, AcceptsStrategyViewReady(events.MergePeriodCompleted.Name(), false), "strategies without factor Views may consume input readiness")
}

func openPeriodBarrier(t *testing.T) (*PeriodBarrier, *recordingFactorReporter) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	reports := new(recordingFactorReporter)
	barrier, err := NewPeriodBarrier(db, reports)
	require.NoError(t, err)
	return barrier, reports
}

func testBarrierKey(period time.Time) PeriodKey {
	return PeriodKey{
		SpaceID: "space", DatasetID: "mdataset_binance_kline_1m", SnapshotID: "snap-1",
		Frequency: "1m", PeriodTime: period.Unix(),
	}
}

func testFrozenBinding(bindingID, factorID, factorType string, subjects []string) FrozenBinding {
	return FrozenBinding{
		BindingID: bindingID, FactorID: factorID, FactorType: factorType,
		SourceHash: "hash-" + factorID, Subjects: subjects,
	}
}

type recordingFactorReporter struct {
	markers []*storagepb.FactorPeriodComputedMarker
}

func (r *recordingFactorReporter) ReportFactorPeriodComputed(_ context.Context, _ string, marker *storagepb.FactorPeriodComputedMarker) error {
	copied := marker
	r.markers = append(r.markers, copied)
	return nil
}
