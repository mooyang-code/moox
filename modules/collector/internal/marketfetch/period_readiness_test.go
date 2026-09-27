package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"path/filepath"
)

func TestPeriodReadinessServiceUsesRowDataTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule", TaskName: "rule", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStateReady}))
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "task-btc", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-1",
	}, {
		SpaceID: "crypto", InstanceID: "task-btc-duplicate", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-2",
	}}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-btc", SpaceID: "crypto", InstanceID: "task-btc", TaskID: "rule", DatasetID: "bars", Status: "pending"},
		{ID: "target-btc-duplicate", SpaceID: "crypto", InstanceID: "task-btc-duplicate", TaskID: "rule", DatasetID: "bars", Status: "pending"},
	}))
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	require.NoError(t, service.EnsureCurrentAndNext(ctx, "crypto", time.Date(2026, 8, 9, 12, 3, 45, 0, time.UTC)))
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-2",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}}}},
	}))
	// The next period is pre-seeded, so a row arriving at the boundary is not
	// acknowledged against a missing parent before the next timer tick.
	nextPeriod := period.Add(time.Minute)
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-2",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: nextPeriod.Format(time.RFC3339Nano)}}}}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, period.Add(time.Minute+2*time.Second), 10)
	require.NoError(t, err)
	var found bool
	found = false
	for _, report := range reports {
		if report.Readiness.PeriodTime.Equal(period) {
			found = true
			require.Equal(t, domain.PeriodStatusComplete, report.Readiness.Status)
		}
	}
	require.True(t, found)
	found = false
	for _, report := range reports {
		if report.Readiness.PeriodTime.Equal(nextPeriod) {
			found = true
			require.Equal(t, domain.PeriodStatusComplete, report.Readiness.Status)
		}
	}
	require.True(t, found)
}

func TestSharedInstanceBuildsIndependentReadinessPerWriteTarget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", Enabled: true},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	instance := domain.TaskInstance{
		SpaceID: "crypto", InstanceID: "shared-btc", CollectionTaskID: "task-a", DataType: "kline",
		DatasetID: "bars-a", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-1",
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-a", DatasetID: "bars-a", OutputFields: `["close"]`, Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-b", DatasetID: "bars-b", OutputFields: `["close","volume"]`, Status: "pending"},
	}))

	now := time.Date(2026, 8, 9, 12, 3, 45, 0, time.UTC)
	windows, err := periodWindows(now, "1m")
	require.NoError(t, err)
	period := windows[0].PeriodTime
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.EnsureCurrentAndNext(ctx, "crypto", now))

	// Target A asks only for close and can complete immediately.
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars-a", WriteSource: "scf:fetch-1",
		Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}},
		}},
	}))
	// Target B needs close+volume, so close alone must not complete it.
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars-b", WriteSource: "scf:fetch-1",
		Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}},
		}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, windows[0].CloseAt.Add(500*time.Millisecond), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "bars-a", reports[0].Readiness.DatasetID)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
	require.Equal(t, "target-a", reports[0].Items[0].WriteTargetID)

	// Completing B's target later must produce an independent Dataset report.
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars-b", WriteSource: "scf:fetch-1",
		Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}, {FieldId: "volume"}},
		}},
	}))
	reports, err = db.PeriodReadiness().FinalizeDue(ctx, windows[0].CloseAt.Add(500*time.Millisecond), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "bars-b", reports[0].Readiness.DatasetID)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
	require.Equal(t, "target-b", reports[0].Items[0].WriteTargetID)
}

func TestPeriodReadinessSeparatesSameSubjectBySeriesTag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task", TaskName: "task", DataType: "kline", Enabled: true, PrepareState: domain.PrepareStateReady}))
	instances := []domain.TaskInstance{
		{SpaceID: "crypto", InstanceID: "binance-btc", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-1"},
		{SpaceID: "crypto", InstanceID: "okx-btc", Provider: "okx", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", FunctionName: "fetch-1"},
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, instances))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-binance", SpaceID: "crypto", InstanceID: "binance-btc", TaskID: "task", DatasetID: "bars", OutputFields: `["close"]`, Status: "pending"},
		{ID: "target-okx", SpaceID: "crypto", InstanceID: "okx-btc", TaskID: "task", DatasetID: "bars", OutputFields: `["close"]`, Status: "pending"},
	}))

	now := time.Date(2026, 8, 9, 12, 3, 45, 0, time.UTC)
	windows, err := periodWindows(now, "1m")
	require.NoError(t, err)
	period := windows[0].PeriodTime
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.EnsureCurrentAndNext(ctx, "crypto", now))

	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetch-1",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
				SeriesTag: defaultMarketSeriesTag("binance", "spot_http", "spot"),
			}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}},
		}},
	}))

	reports, err := db.PeriodReadiness().FinalizeDue(ctx, windows[0].CloseAt.Add(2*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusDegraded, reports[0].Readiness.Status)
	require.Len(t, reports[0].Items, 2)
	states := map[string]string{}
	for _, item := range reports[0].Items {
		states[item.SeriesTag] = item.State
	}
	require.Equal(t, domain.PeriodItemSuccess, states[defaultMarketSeriesTag("binance", "spot_http", "spot")])
	require.Equal(t, domain.PeriodItemTimedOut, states[defaultMarketSeriesTag("okx", "spot_http", "spot")])
}

func TestApplyRowsMarksSubjectAfterTimerReassignment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err = db.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{{
			InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "nanjing-25",
			WriteSource: "scf:nanjing-25", RequiredFields: `["open","high","low","close","volume","quote_volume","trade_num"]`,
		}},
	})
	require.NoError(t, err)
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:nanjing-30",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
			}}},
			Fields: []*storagepb.FieldValue{
				{FieldId: "open"}, {FieldId: "high"}, {FieldId: "low"}, {FieldId: "close"},
				{FieldId: "volume"}, {FieldId: "quote_volume"}, {FieldId: "trade_num"},
			},
		}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, period.Add(10*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, domain.PeriodStatusComplete, reports[0].Readiness.Status)
	require.Equal(t, domain.PeriodItemSuccess, reports[0].Items[0].State)
}

func TestApplyRowsKeepsPendingWhenKlineFieldsAreIncomplete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	key := domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period}
	_, err = db.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey: key, DeadlineAt: period.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{{
			InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", FunctionName: "nanjing-25",
			WriteSource: "scf:nanjing-25", RequiredFields: `["open","high","low","close","volume","quote_volume","trade_num"]`,
		}},
	})
	require.NoError(t, err)
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:nanjing-25",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
			}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}, {FieldId: "volume"}},
		}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, period.Add(10*time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, reports)
}

func TestSelectedKlineFieldsCompletePeriodReadiness(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	period := time.Date(2026, 8, 9, 12, 3, 0, 0, time.UTC)
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", InstanceID: "task-btc", CollectionTaskID: "selected", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m",
		TaskParams: `{"output_fields":["close","volume"]}`,
	}}))
	windows, err := periodWindows(period.Add(45*time.Second), "1m")
	require.NoError(t, err)
	_, err = db.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey: domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: windows[0].PeriodTime}, DeadlineAt: windows[0].CloseAt.Add(time.Minute),
		Tasks: []domain.PeriodTaskSeed{{InstanceID: "task-btc", WriteTargetID: "task-btc", SubjectID: "BTC-USDT", RequiredFields: `[]`}},
	})
	require.NoError(t, err)
	service := NewPeriodReadinessService(db.TaskInstances(), db.PeriodReadiness(), time.Second)
	require.NoError(t, service.ApplyRows(ctx, &storagepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetcher-1", Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: windows[0].PeriodTime.Format(time.RFC3339Nano)}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close"}, {FieldId: "volume"}},
		}},
	}))
	reports, err := db.PeriodReadiness().FinalizeDue(ctx, windows[0].CloseAt.Add(time.Minute+2*time.Second), 10)
	require.NoError(t, err)
	var found bool
	for _, report := range reports {
		if report.Readiness.PeriodTime.Equal(windows[0].PeriodTime) {
			found = true
			require.Equal(t, domain.PeriodStatusComplete, report.Readiness.Status)
		}
	}
	require.True(t, found)
}
