package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func TestPeriodStorageReconcilerCountsRetainCommittedManifestAfterSnapshotRollback(t *testing.T) {
	db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: t.TempDir() + "/counts.db?_time_format=sqlite"}, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(collectorschema.AllSQL()).Error)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	period := now.Add(-40 * 24 * time.Hour)
	cutoff := now.Add(-periodStorageRetention)
	snapshots := store.NewPeriodSeriesSnapshotRepository(db)
	states := store.NewPeriodStorageStateRepository(db)
	snapshot, _, err := snapshots.CreatePeriodSeriesSnapshotIfAbsent(ctx, reconcilerSnapshot("crypto", "bars", "1m", period, "BTC-USDT", "ETH-USDT"))
	require.NoError(t, err)
	require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).Where("c_space_id = ?", "crypto").Update("c_ctime", cutoff.Add(-time.Hour)).Error)
	state := reconcilerStateForSnapshot(snapshot, domain.PeriodStatusComplete)
	state.ConfirmedAt = cutoff.Add(-time.Hour)
	require.NoError(t, states.ObservePeriodStorageState(ctx, state))
	require.NoError(t, db.Create(&domain.BatchInvocation{SpaceID: "crypto", BatchID: "batch-counts", ScheduleID: "batch-counts", BatchKind: domain.BatchKindRealtime, Status: domain.BatchStatusSucceeded}).Error)
	require.NoError(t, db.Create(&domain.TimerPeriodBatch{Key: "manifest-counts", SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount, GroupCount: 1, BatchID: "batch-counts", DeadlineAt: now}).Error)
	client := &periodStatusStub{status: func(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		return state, nil
	}}
	r := NewPeriodStorageReconciler(snapshots, states, client, "crypto", store.NewTimerPeriodBatchRepository(db))
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_snapshot_state_delete BEFORE DELETE ON t_collector_period_storage_states BEGIN SELECT RAISE(ABORT, 'test rollback'); END`).Error)
	counts, err := r.ReconcileWithCleanupCounts(ctx, now, periodStorageRetention, 3, 1)
	require.Error(t, err)
	require.Equal(t, store.PeriodCleanupCounts{ManifestRows: 1}, counts)
	var entries int64
	require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).Count(&entries).Error)
	require.EqualValues(t, 2, entries)
	require.NoError(t, db.Exec(`DROP TRIGGER fail_snapshot_state_delete`).Error)
	counts, err = r.ReconcileWithCleanupCounts(ctx, now, periodStorageRetention, 3, 1)
	require.NoError(t, err)
	require.Equal(t, store.PeriodCleanupCounts{SnapshotRows: 2, StateRows: 1}, counts)
}

func TestPeriodStorageReconcilerProbesSnapshotIdentityAndDeletesOnlyConfirmedTerminal(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	period := now.Add(-31 * 24 * time.Hour)
	snapshot := reconcilerSnapshot("crypto", "bars", "1h", period, "BTC-USDT", "ETH-USDT")
	stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		require.Equal(t, "crypto", expectation.GetSpaceId())
		require.Equal(t, "bars", expectation.GetDatasetId())
		require.Equal(t, "1h", expectation.GetFrequency())
		require.Equal(t, period.Unix(), expectation.GetPeriodTime())
		require.Equal(t, stored.SeriesHash, expectation.GetSeriesHash())
		require.Equal(t, stored.ExpectedCount, expectation.GetExpectedCount())
		require.Zero(t, expectation.GetDeadlineAt(), "status probes do not invent a deadline")
		require.Empty(t, expectation.GetSeriesSnapshot(), "status probes use the persisted snapshot identity, not a reconstructed series list")
		return reconcilerStateForSnapshot(stored, domain.PeriodStatusComplete), nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.Zero(t, deleted, "a fresh snapshot for an old market period must outlive Storage completion")
	require.EqualValues(t, 1, client.calls.Load())
	_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, stored.Key)
	require.NoError(t, err)
	require.True(t, found, "retention is based on snapshot creation time, not the market period timestamp")
	state, found, err := db.PeriodStorageStates().GetPeriodStorageState(ctx, stored.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusComplete, state.Status)
}

func TestPeriodStorageReconcilerRefreshesRecentWaitingState(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	snapshot := reconcilerSnapshot("crypto", "bars", "1m", now.Add(-time.Minute), "BTC-USDT")
	stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	waiting := reconcilerStateForSnapshot(stored, domain.PeriodStatusWaiting)
	require.NoError(t, db.PeriodStorageStates().ObservePeriodStorageState(ctx, waiting))
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		require.Equal(t, stored.Key.DatasetID, expectation.GetDatasetId())
		require.Equal(t, stored.SeriesHash, expectation.GetSeriesHash())
		require.Equal(t, stored.ExpectedCount, expectation.GetExpectedCount())
		complete := waiting
		complete.Status = domain.PeriodStatusComplete
		complete.ConfirmedAt = now
		return complete, nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	_, err = reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.calls.Load(), "recent waiting periods are reconciled without waiting for retention expiry")
	state, found, err := db.PeriodStorageStates().GetPeriodStorageState(ctx, stored.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusComplete, state.Status)
}

func TestPeriodStorageReconcilerReportsBoundedProbeMetrics(t *testing.T) {
	db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: t.TempDir() + "/probe-metrics.db?_time_format=sqlite"}, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(collectorschema.AllSQL()).Error)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	snapshots := store.NewPeriodSeriesSnapshotRepository(db)
	states := store.NewPeriodStorageStateRepository(db)
	oldSnapshot, _, err := snapshots.CreatePeriodSeriesSnapshotIfAbsent(ctx,
		reconcilerSnapshot("crypto", "old-bars", "1m", now.Add(-40*24*time.Hour), "BTC-USDT"))
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE t_collector_task_period_series SET c_ctime = ? WHERE c_space_id = ? AND c_dataset_id = ?", now.Add(-40*24*time.Hour), "crypto", "old-bars").Error)
	waitingSnapshot, _, err := snapshots.CreatePeriodSeriesSnapshotIfAbsent(ctx,
		reconcilerSnapshot("crypto", "waiting-bars", "1m", now.Add(-time.Minute), "ETH-USDT"))
	require.NoError(t, err)
	require.NoError(t, states.ObservePeriodStorageState(ctx, reconcilerStateForSnapshot(waitingSnapshot, domain.PeriodStatusWaiting)))
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		snapshot := oldSnapshot
		if expectation.GetDatasetId() == "waiting-bars" {
			snapshot = waitingSnapshot
		}
		return reconcilerStateForSnapshot(snapshot, domain.PeriodStatusComplete), nil
	}}
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	reconciler := NewPeriodStorageReconciler(snapshots, states, client, "crypto")
	reconciler.Metrics = metrics
	_, err = reconciler.ReconcileWithCleanupCounts(ctx, now, periodStorageRetention, 100, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, testutil.ToFloat64(metrics.periodStorageStatusProbes.WithLabelValues("crypto", "success")))
	require.Zero(t, testutil.ToFloat64(metrics.periodStorageStatusProbes.WithLabelValues("crypto", "error")))
	require.GreaterOrEqual(t, testutil.ToFloat64(metrics.periodStorageReconcileDuration.WithLabelValues("crypto")), 0.0)
}

func TestPeriodStorageReconcilerDefersWaitingUnknownAndErrorStates(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	periods := []time.Time{now.Add(-33 * 24 * time.Hour), now.Add(-32 * 24 * time.Hour), now.Add(-31 * 24 * time.Hour)}
	snapshots := make([]domain.PeriodSeriesSnapshot, 0, len(periods))
	for i, period := range periods {
		snapshot := reconcilerSnapshot("crypto", "bars", "1m", period, fmt.Sprintf("S%d", i))
		stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		snapshots = append(snapshots, stored)
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		period := time.Unix(expectation.GetPeriodTime(), 0).UTC()
		switch period {
		case periods[0]:
			return domain.PeriodStorageState{}, errors.New("status unavailable")
		case periods[1]:
			return reconcilerStateForSnapshot(snapshots[1], domain.PeriodStatusWaiting), nil
		default:
			return domain.PeriodStorageState{}, errors.New("NOT_FOUND")
		}
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.Error(t, err, "Storage query failures are reported for operator visibility")
	require.Zero(t, deleted)
	for index, snapshot := range snapshots {
		_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, snapshot.Key)
		require.NoError(t, err)
		require.True(t, found, "unconfirmed candidate %d remains", index)
	}
	state, found, err := db.PeriodStorageStates().GetPeriodStorageState(ctx, snapshots[1].Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, domain.PeriodStatusWaiting, state.Status)
}

func TestPeriodStorageReconcilerAdvancesPastPersistentFirstPageFailures(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := base.Add(32 * 24 * time.Hour)
	const candidateCount = periodStorageReconcileCandidateLimit + 1
	var finalSnapshot domain.PeriodSeriesSnapshot
	for index := 0; index < candidateCount; index++ {
		period := base.Add(time.Duration(index) * time.Minute)
		snapshot := reconcilerSnapshot("crypto", "bars", "1m", period, fmt.Sprintf("S%04d", index))
		stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		if index == candidateCount-1 {
			finalSnapshot = stored
		}
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		if expectation.GetPeriodTime() != finalSnapshot.Key.PeriodTime.Unix() {
			return domain.PeriodStorageState{}, errors.New("persistent NOT_FOUND")
		}
		return reconcilerStateForSnapshot(finalSnapshot, domain.PeriodStatusDegraded), nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.Error(t, err)
	require.Zero(t, deleted)
	require.EqualValues(t, periodStorageReconcileCandidateLimit, client.calls.Load())
	_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, finalSnapshot.Key)
	require.NoError(t, err)
	require.True(t, found, "the last candidate is intentionally deferred to the next round")

	deleted, err = reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.Zero(t, deleted, "recently created snapshots remain retained after a terminal probe")
	require.EqualValues(t, candidateCount, client.calls.Load())
	_, found, err = db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, finalSnapshot.Key)
	require.NoError(t, err)
	require.True(t, found, "the next keyset page reaches the terminal candidate without violating retention")
}

func TestPeriodStorageReconcilerCleansBoundedTerminalPagePerPass(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(31 * 24 * time.Hour)
	periods := make([]domain.PeriodSeriesSnapshot, periodStorageReconcileCandidateLimit+1)
	for index := range periods {
		period := now.Add(-31*24*time.Hour + time.Duration(index)*time.Minute)
		snapshot := reconcilerSnapshot("crypto", "bars", "1m", period, fmt.Sprintf("S%02d", index))
		stored, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
		require.NoError(t, err)
		periods[index] = stored
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		for _, snapshot := range periods {
			if expectation.GetPeriodTime() == snapshot.Key.PeriodTime.Unix() {
				state := reconcilerStateForSnapshot(snapshot, domain.PeriodStatusComplete)
				state.ConfirmedAt = now.Add(-31 * 24 * time.Hour)
				return state, nil
			}
		}
		return domain.PeriodStorageState{}, fmt.Errorf("unknown period %d", expectation.GetPeriodTime())
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	deleted, err := reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, periodStorageCleanupRowLimit, deleted)
	require.EqualValues(t, periodStorageReconcileCandidateLimit, client.calls.Load())
	deleted, err = reconciler.Reconcile(ctx, now)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted, "one series entry and its Storage state count as two physical rows")
	require.EqualValues(t, len(periods), client.calls.Load())
}

func TestPeriodStorageReconcilerUsesBoundedConcurrentProbes(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	const periodCount = 20
	for index := range periodCount {
		state := domain.PeriodStorageState{
			Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: fmt.Sprintf("bars-%02d", index), Frequency: "1m", PeriodTime: now},
			SeriesHash: "series", ExpectedCount: 1, DeadlineAt: now.Add(time.Minute), Status: domain.PeriodStatusWaiting, ConfirmedAt: now,
		}
		require.NoError(t, db.PeriodStorageStates().ObservePeriodStorageState(ctx, state))
	}
	entered := make(chan struct{}, periodCount)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	client := &periodStatusStub{status: func(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			active.Add(-1)
			return domain.PeriodStorageState{}, ctx.Err()
		case <-release:
		}
		active.Add(-1)
		return domain.PeriodStorageState{
			Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: time.Unix(expectation.GetPeriodTime(), 0).UTC()},
			SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
			DeadlineAt: now.Add(time.Minute), Status: domain.PeriodStatusComplete, ConfirmedAt: now.Add(time.Second),
		}, nil
	}}
	reconciler := NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), client, "crypto")
	result := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(ctx, now)
		result <- err
	}()
	for range periodStorageStatusProbeConcurrency {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			<-result
			t.Fatalf("Storage probes did not reach the configured concurrency: %v", ctx.Err())
		}
	}
	require.EqualValues(t, periodStorageStatusProbeConcurrency, maxActive.Load())
	close(release)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("Storage reconciliation did not finish: %v", ctx.Err())
	}
}

func TestPeriodStorageReconcilerAccountsSnapshotRowsAndTimerManifestsSeparately(t *testing.T) {
	db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: t.TempDir() + "/cleanup-budget.db?_time_format=sqlite"}, &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(collectorschema.AllSQL()).Error)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-periodStorageRetention)
	snapshots := store.NewPeriodSeriesSnapshotRepository(db)
	states := store.NewPeriodStorageStateRepository(db)
	batches := store.NewTimerPeriodBatchRepository(db)
	periods := []struct {
		period   time.Time
		subjects []string
	}{
		{period: now.Add(-40 * 24 * time.Hour), subjects: []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}},
		{period: now.Add(-39 * 24 * time.Hour), subjects: []string{"ADA-USDT", "XRP-USDT"}},
	}
	for index, fixture := range periods {
		snapshot, _, err := snapshots.CreatePeriodSeriesSnapshotIfAbsent(ctx,
			reconcilerSnapshot("crypto", "bars", "1m", fixture.period, fixture.subjects...))
		require.NoError(t, err)
		require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).
			Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", "crypto", "bars", "1m", fixture.period).
			Update("c_ctime", cutoff.Add(-time.Hour)).Error)
		require.NoError(t, states.ObservePeriodStorageState(ctx, domain.PeriodStorageState{
			Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
			DeadlineAt: fixture.period.Add(time.Minute), Status: domain.PeriodStatusComplete, ConfirmedAt: cutoff.Add(-time.Hour),
		}))
		batchID := fmt.Sprintf("terminal-period-batch-%d", index)
		completedAt := fixture.period.Add(time.Hour)
		require.NoError(t, db.Create(&domain.BatchInvocation{
			SpaceID: "crypto", BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime,
			Frequency: "1m", Status: domain.BatchStatusSucceeded, CompletedAt: &completedAt,
		}).Error)
		require.NoError(t, db.Create(&domain.TimerPeriodBatch{
			Key: fmt.Sprintf("terminal-period-manifest-%d", index), SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: fixture.period,
			TaskID: fmt.Sprintf("task-%d", index), FirstRunID: fmt.Sprintf("run-%d", index), SeriesHash: snapshot.SeriesHash,
			ExpectedCount: snapshot.ExpectedCount, GroupCount: 1, BindingHash: "binding", RouteVersion: "route-v1", BatchID: batchID,
			FunctionName: "timer-function", NodeID: "timer-node", Region: "region", DeadlineAt: fixture.period.Add(time.Hour),
		}).Error)
	}
	client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
		period := time.Unix(expectation.GetPeriodTime(), 0).UTC()
		return domain.PeriodStorageState{
			Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: period},
			SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
			DeadlineAt: period.Add(time.Minute), Status: domain.PeriodStatusComplete, ConfirmedAt: cutoff.Add(-time.Hour),
		}, nil
	}}
	reconciler := NewPeriodStorageReconciler(snapshots, states, client, "crypto", batches)
	physicalRows := func() int64 {
		var snapshots, states, manifests int64
		require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).Count(&snapshots).Error)
		require.NoError(t, db.Table("t_collector_period_storage_states").Count(&states).Error)
		require.NoError(t, db.Model(&domain.TimerPeriodBatch{}).Count(&manifests).Error)
		return snapshots + states + manifests
	}
	before := physicalRows()
	counts, err := reconciler.ReconcileWithCleanupCounts(ctx, now, periodStorageRetention, 5, 1)
	deleted := counts.SnapshotRows + counts.StateRows
	require.NoError(t, err)
	require.Equal(t, store.PeriodCleanupCounts{SnapshotRows: 3, StateRows: 1, ManifestRows: 1}, counts)
	require.EqualValues(t, 4, deleted, "the first terminal period removes three entries and one state row")
	require.EqualValues(t, 5, before-physicalRows(), "snapshot rows honor their budget while one manifest uses its separate budget")
	_, found, err := snapshots.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: periods[0].period})
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = snapshots.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: periods[1].period})
	require.NoError(t, err)
	require.True(t, found, "the second period remains protected while its manifest is outside the manifest budget")

	before = physicalRows()
	deleted, err = reconciler.ReconcileWithLimits(ctx, now, periodStorageRetention, 5, 1)
	require.NoError(t, err)
	require.EqualValues(t, 3, deleted, "the second period removes two entries and its state row")
	require.EqualValues(t, 4, before-physicalRows(), "the second invocation remains within both independent budgets")
	_, found, err = snapshots.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: periods[1].period})
	require.NoError(t, err)
	require.False(t, found)
}

func TestPeriodStorageReconcilerBudgetExceedsSustainedArrivalRate(t *testing.T) {
	require.Equal(t, 500, periodStorageReconcileCandidateLimit)
	require.Equal(t, 500, periodStorageWaitingProbeLimit)
	require.Equal(t, 1000, periodStorageCleanupRowLimit)
	require.Equal(t, 10, periodStorageStatusProbeConcurrency)
	const initialBacklog, arrivalsPerMinute, waitingCount, minutes = 900, 300, 3, 8
	for _, cleanupLimit := range []int{100, periodStorageCleanupRowLimit} {
		t.Run(fmt.Sprintf("cleanup_limit_%d", cleanupLimit), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: t.TempDir() + "/backlog.db?_time_format=sqlite"}, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.Exec(collectorschema.AllSQL()).Error)
			ctx := context.Background()
			base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
			oldest := base.Add(-31 * 24 * time.Hour)
			snapshots := store.NewPeriodSeriesSnapshotRepository(db)
			states := store.NewPeriodStorageStateRepository(db)
			var next int
			addSnapshots := func(count int) {
				entries := make([]domain.PeriodSeriesSnapshotEntry, 0, count)
				for range count {
					period := oldest.Add(time.Duration(next+waitingCount) * time.Second)
					frequency := []string{"1m", "1h"}[next%2]
					snapshot := reconcilerSnapshot("crypto", fmt.Sprintf("bars-%d", next%3), frequency, period, "BTC-USDT")
					snapshot.Entries[0].CreateTime = period
					entries = append(entries, snapshot.Entries[0])
					next++
				}
				// Seed aged fixture rows directly because repository creation uses wall-clock time.
				require.NoError(t, db.CreateInBatches(entries, 500).Error)
			}
			waiting := make([]domain.PeriodSeriesSnapshot, waitingCount)
			for index := range waiting {
				waiting[index] = reconcilerSnapshot("crypto", "waiting", "1m", oldest.Add(time.Duration(index)*time.Second), "BTC-USDT")
				waiting[index].Entries[0].CreateTime = waiting[index].Key.PeriodTime
				require.NoError(t, db.Create(&waiting[index].Entries).Error)
				require.NoError(t, states.ObservePeriodStorageState(ctx, reconcilerStateForSnapshot(waiting[index], domain.PeriodStatusWaiting)))
			}
			addSnapshots(initialBacklog)
			var terminalWaiting atomic.Bool
			client := &periodStatusStub{status: func(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
				period := time.Unix(expectation.GetPeriodTime(), 0).UTC()
				status := domain.PeriodStatusComplete
				if expectation.GetDatasetId() == "waiting" {
					status = domain.PeriodStatusWaiting
					if terminalWaiting.Load() {
						status = domain.PeriodStatusDegraded
					}
				}
				// Storage returns historical confirmations, already outside the retention window.
				return domain.PeriodStorageState{
					Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: period},
					SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
					DeadlineAt: period.Add(time.Minute), ConfirmedAt: period.Add(time.Hour), Status: status,
				}, nil
			}}
			reconciler := NewPeriodStorageReconciler(snapshots, states, client, "crypto")
			var remaining int64
			var totalDeletedEntries int64
			for minute := range minutes {
				now := base.Add(time.Duration(minute) * time.Minute)
				addSnapshots(arrivalsPerMinute)
				var beforeCleanup int64
				require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).Count(&beforeCleanup).Error)
				terminalWaiting.Store(minute >= 6)
				beforeCalls := client.calls.Load()
				started := time.Now()
				deleted, err := reconciler.ReconcileWithLimits(ctx, now, periodStorageRetention, cleanupLimit, 0)
				elapsed := time.Since(started)
				require.NoError(t, err)
				require.LessOrEqual(t, deleted, int64(cleanupLimit))
				calls := client.calls.Load() - beforeCalls
				require.LessOrEqual(t, calls, int64(periodStorageReconcileCandidateLimit+periodStorageWaitingProbeLimit))
				if minute == 0 {
					require.EqualValues(t, periodStorageReconcileCandidateLimit+waitingCount, calls, "the overdue backlog saturates the snapshot probe budget")
				}
				require.NoError(t, db.Model(&domain.PeriodSeriesSnapshotEntry{}).Count(&remaining).Error)
				totalDeletedEntries += beforeCleanup - remaining
				require.EqualValues(t, initialBacklog+waitingCount+(minute+1)*arrivalsPerMinute-int(totalDeletedEntries), remaining)
				var oldestAge time.Duration
				if remaining > 0 {
					var entry domain.PeriodSeriesSnapshotEntry
					require.NoError(t, db.Order("c_ctime").First(&entry).Error)
					oldestAge = now.Sub(entry.CreateTime)
				}
				t.Logf("minute=%d added=%d rpc=%d deleted=%d backlog=%d cleanup_elapsed=%s oldest_snapshot_age=%s", minute+1, arrivalsPerMinute, calls, deleted, remaining, elapsed, oldestAge)
				for _, snapshot := range waiting {
					_, found, err := snapshots.GetPeriodSeriesSnapshot(ctx, snapshot.Key)
					require.NoError(t, err)
					if !terminalWaiting.Load() {
						require.True(t, found, "overdue waiting snapshots must survive until Storage confirms terminal status")
						state, found, err := states.GetPeriodStorageState(ctx, snapshot.Key)
						require.NoError(t, err)
						require.True(t, found)
						require.Equal(t, domain.PeriodStatusWaiting, state.Status)
					} else if cleanupLimit == periodStorageCleanupRowLimit {
						require.False(t, found, "terminal confirmation releases the retained waiting snapshot")
					}
				}
				if cleanupLimit == periodStorageCleanupRowLimit && minute == 5 {
					require.EqualValues(t, waitingCount, remaining, "the drainable backlog converges despite continued arrivals")
				}
			}
			if cleanupLimit == periodStorageCleanupRowLimit {
				require.Zero(t, remaining, "the backlog drains and remains empty under sustained 300/minute arrivals")
			} else {
				require.Greater(t, remaining, int64(initialBacklog), "a 100/minute cleanup budget cannot keep up with 300/minute arrivals")
			}
		})
	}
}

type periodStatusStub struct {
	calls  atomic.Int64
	status func(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
}

func (s *periodStatusStub) GetDatasetPeriodStatus(ctx context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.calls.Add(1)
	return s.status(ctx, expectation)
}

var _ PeriodStorageStatusClient = (*periodStatusStub)(nil)

func reconcilerSnapshot(spaceID, datasetID, frequency string, period time.Time, subjects ...string) domain.PeriodSeriesSnapshot {
	entries := make([]domain.PeriodSeriesSnapshotEntry, 0, len(subjects))
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		tag := "venue:binance|market:spot|source:spot_http"
		key := domain.CanonicalSeriesKey("binance", "spot_http", "spot", subject, tag)
		entries = append(entries, domain.PeriodSeriesSnapshotEntry{
			SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period,
			SeriesIndex: uint32(len(entries)), SeriesKey: key, SubjectID: subject, Provider: "binance", SourceID: "spot_http",
			MarketType: "spot", ProviderSymbol: subject, SeriesTag: tag,
		})
		keys = append(keys, key)
	}
	hash := domain.SeriesSetHash(keys)
	for i := range entries {
		entries[i].SeriesHash = hash
		entries[i].ExpectedCount = len(entries)
	}
	return domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period},
		SeriesHash: hash, ExpectedCount: uint32(len(entries)), Entries: entries,
	}
}

func reconcilerStateForSnapshot(snapshot domain.PeriodSeriesSnapshot, status string) domain.PeriodStorageState {
	return domain.PeriodStorageState{
		Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		DeadlineAt: snapshot.Key.PeriodTime.Add(time.Minute), Status: status, ConfirmedAt: snapshot.Key.PeriodTime.Add(time.Hour),
	}
}

func (r *PeriodStorageReconciler) ReconcileWithLimits(ctx context.Context, now time.Time, retention time.Duration, cleanupRowBudget, manifestLimit int) (int64, error) {
	counts, err := r.ReconcileWithCleanupCounts(ctx, now, retention, cleanupRowBudget, manifestLimit)
	return counts.SnapshotRows + counts.StateRows, err
}

// Reconcile probes a bounded page of expired snapshot periods in stable keyset
// order, then deletes confirmed terminal snapshots. Failed probes advance the
// cursor and are retried after the scan wraps on a later round.
func (r *PeriodStorageReconciler) Reconcile(ctx context.Context, now time.Time) (int64, error) {
	return r.ReconcileWithLimits(ctx, now, periodStorageRetention, periodStorageCleanupRowLimit, periodStorageCleanupManifestLimit)
}
