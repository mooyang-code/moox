package marketfetch

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestTimerPeriodPlannerReusesFrozenOwnerAcrossRuns(t *testing.T) {
	ctx := context.Background()
	s := newTimerPlannerStore(t)
	task := timerPlannerTask()
	require.NoError(t, s.Tasks().Create(ctx, task))
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	plan := timerPlannerPlan(task, "run-first", period)
	ensureCalls := 0
	planner := TimerPeriodPlanner{
		Snapshots: s.PeriodSeriesSnapshot(), States: s.PeriodStorageStates(), Batches: s.TimerPeriodBatches(),
		Now: func() time.Time { return period.Add(10 * time.Second) },
		EnsureStorage: func(_ context.Context, snapshot domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			ensureCalls++
			require.Equal(t, uint32(1), snapshot.ExpectedCount)
			return timerPlannerStorageState(snapshot, period.Add(2*time.Hour)), nil
		},
	}
	created, err := planner.Plan(ctx, plan)
	require.NoError(t, err)
	require.True(t, created)

	second := timerPlannerPlan(task, "run-after-restart", period)
	second.Instances[0].InstanceID = "new-run-instance"
	second.Instances[0].RunID = "run-after-restart"
	second.Request.Items[0].InstanceID = "new-run-instance"
	second.Targets[0].InstanceID = "new-run-instance"
	created, err = planner.Plan(ctx, second)
	require.NoError(t, err)
	require.False(t, created, "an existing period manifest is immutable and must not append a later Run")
	require.Equal(t, 2, ensureCalls, "Storage is ensured from the same durable snapshot on restart")

	manifest, err := s.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
	require.NoError(t, err)
	require.Equal(t, "run-first", manifest.FirstRunID)
	require.Equal(t, stableID("crypto", "bars", "1m", period.Format(time.RFC3339Nano), "0", "timer-initial"), manifest.BatchID)
	batch, err := s.FetchBatches().Get(ctx, "crypto", manifest.BatchID)
	require.NoError(t, err)
	require.Equal(t, 1, batch.PlannedCount)
	items, err := s.FetchBatches().ListItems(ctx, "crypto", manifest.BatchID)
	require.NoError(t, err)
	require.Equal(t, []string{"instance-old"}, items)
}

func TestTimerPeriodPlannerFailedEnsureCreatesNoInitialBatch(t *testing.T) {
	ctx := context.Background()
	s := newTimerPlannerStore(t)
	task := timerPlannerTask()
	require.NoError(t, s.Tasks().Create(ctx, task))
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	planner := TimerPeriodPlanner{
		Snapshots: s.PeriodSeriesSnapshot(), States: s.PeriodStorageStates(), Batches: s.TimerPeriodBatches(),
		Now: func() time.Time { return period.Add(10 * time.Second) },
		EnsureStorage: func(context.Context, domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return domain.PeriodStorageState{}, errors.New("storage unavailable")
		},
	}
	_, err := planner.Plan(ctx, timerPlannerPlan(task, "run", period))
	require.ErrorContains(t, err, "storage unavailable")
	_, err = s.FetchBatches().Get(ctx, "crypto", stableID("crypto", "bars", "1m", period.Format(time.RFC3339Nano), "0", "timer-initial"))
	require.Error(t, err, "Storage Ensure must finish before any initial batch is persisted")
	_, err = s.TimerPeriodBatches().GetByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}, 0)
	require.Error(t, err)
}

func TestTimerPeriodPlannerRejectsDeadlineConsumedDuringEnsure(t *testing.T) {
	ctx := context.Background()
	s := newTimerPlannerStore(t)
	task := timerPlannerTask()
	require.NoError(t, s.Tasks().Create(ctx, task))
	period := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	clock := period.Add(10 * time.Second)
	deadline := period.Add(time.Minute)
	planner := TimerPeriodPlanner{
		Snapshots: s.PeriodSeriesSnapshot(), States: s.PeriodStorageStates(), Batches: s.TimerPeriodBatches(),
		Now: func() time.Time { return clock },
		EnsureStorage: func(_ context.Context, snapshot domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			clock = deadline
			return timerPlannerStorageState(snapshot, deadline), nil
		},
	}
	created, err := planner.Plan(ctx, timerPlannerPlan(task, "run", period))
	require.NoError(t, err)
	require.False(t, created)
	manifests, err := s.TimerPeriodBatches().ListByPeriod(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period})
	require.NoError(t, err)
	require.Empty(t, manifests, "Storage Ensure can consume the remaining dispatch window")
	_, err = s.FetchBatches().Get(ctx, "crypto", stableID("crypto", "bars", "1m", period.Format(time.RFC3339Nano), "0", "timer-initial"))
	require.Error(t, err)
}

func TestTimerPeriodPlannerRejectsInvalidCalendarPeriod(t *testing.T) {
	ctx := context.Background()
	s := newTimerPlannerStore(t)
	task := timerPlannerTask()
	require.NoError(t, s.Tasks().Create(ctx, task))
	period := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	planner := TimerPeriodPlanner{
		Snapshots: s.PeriodSeriesSnapshot(), States: s.PeriodStorageStates(), Batches: s.TimerPeriodBatches(),
		Now:                 func() time.Time { return period.Add(time.Second) },
		ValidTargetDataTime: func(string, time.Time) bool { return false },
		EnsureStorage: func(context.Context, domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			t.Fatal("invalid market-calendar period must be rejected before Storage Ensure")
			return domain.PeriodStorageState{}, nil
		},
	}
	created, err := planner.Plan(ctx, timerPlannerPlan(task, "run", period))
	require.NoError(t, err)
	require.False(t, created)
	_, err = s.FetchBatches().Get(ctx, "crypto", stableID("crypto", "bars", "1m", period.Format(time.RFC3339Nano), "0", "timer-initial"))
	require.Error(t, err)
}

func newTimerPlannerStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func timerPlannerTask() domain.CollectionTask {
	return domain.CollectionTask{SpaceID: "crypto", TaskID: "task-a", TaskName: "Task A", DataType: "kline", CollectParams: `{"frequency":"1m"}`, Enabled: true}
}

func timerPlannerPlan(task domain.CollectionTask, runID string, period time.Time) TimerPeriodPlan {
	seriesKey := domain.CanonicalSeriesKey("binance", "spot_http", "spot", "BTC-USDT", "")
	seriesHash := domain.SeriesSetHash([]string{seriesKey})
	item := domain.CollectionItem{
		InstanceID: "instance-old", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: period.Format(time.RFC3339Nano),
		Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m",
		SeriesIndex: 0, SeriesHash: seriesHash, ExpectedCount: 1,
	}
	targetTime := period.UTC()
	request := Request{
		SpaceID: "crypto", BatchKind: domain.BatchKindRealtime, DatasetID: "bars", Frequency: "1m", Provider: "binance",
		SourceID: "spot_http", MarketType: "spot", Region: "ap-singapore", NodeID: "timer-node", FunctionName: "market-fetch",
		GroupID: 0, GroupCount: 1, Items: []domain.CollectionItem{item},
	}
	return TimerPeriodPlan{
		Task: task, RunID: runID,
		Snapshot: domain.PeriodSeriesSnapshot{
			Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
			SeriesHash: seriesHash, ExpectedCount: 1,
			Entries: []domain.PeriodSeriesSnapshotEntry{{
				SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period, SeriesIndex: 0,
				SeriesKey: seriesKey, SubjectID: "BTC-USDT",
				Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesHash: seriesHash, ExpectedCount: 1,
			}},
		},
		Assignment: NodeAssignment{NodeID: "timer-node", FunctionName: "market-fetch", Region: "ap-singapore", Provider: "binance", RouteProvider: "binance", SourceID: "spot_http", MarketType: "spot", DatasetID: "bars", Frequency: "1m", GroupID: 0, GroupCount: 1, RouteVersion: "route-v1", Enabled: true, Subjects: []string{"BTC-USDT"}},
		Request:    request,
		Instances:  []domain.TaskInstance{{SpaceID: "crypto", InstanceID: item.InstanceID, RunID: runID, Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TargetDataTime: &targetTime, TaskParams: `{}`}},
		Targets:    []domain.WriteTarget{{ID: "target-old", SpaceID: "crypto", InstanceID: item.InstanceID, TaskID: task.TaskID, DatasetID: "bars", SeriesIndex: 0, SeriesHash: seriesHash, ExpectedCount: 1, Status: "pending"}},
	}
}

func timerPlannerStorageState(snapshot domain.PeriodSeriesSnapshot, deadline time.Time) domain.PeriodStorageState {
	return domain.PeriodStorageState{Key: snapshot.Key, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount, DeadlineAt: deadline, Status: domain.PeriodStatusWaiting, ConfirmedAt: deadline.Add(-time.Hour)}
}
