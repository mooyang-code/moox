package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHandleStorageWriteUpdatesAssignedTaskInstances(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule", TaskName: "Rule", DataType: "kline", Enabled: true}))
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "task-btc", CollectionTaskID: "rule", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{{ID: "target-btc", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "rule", DatasetID: "bars", Status: "pending"}}))
	require.NoError(t, db.TaskInstances().AssignMarketFetchFunction(ctx, "crypto", "binance", "spot", "bars", "1m", "fetcher-1", []string{"BTC-USDT"}))
	at := time.Date(2026, 8, 5, 2, 3, 4, 0, time.UTC)
	delivery := &events.EventDelivery{
		Message: &eventpb.EventMessage{OccurredAt: timestamppb.New(at)},
		Payload: &storagepb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetcher-1", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339)}}}}}},
	}
	require.NoError(t, handleStorageWrite(ctx, db.TaskInstances(), delivery))
	stored, err := db.TaskInstances().Get(ctx, "crypto", "task-btc")
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusSuccess, stored.LastExecStatus)
	require.NotNil(t, stored.LastExecTime)
	require.Equal(t, at, stored.LastExecTime.UTC())
}

func TestStorageWriteOnlyUpdatesLatestScheduledRun(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "rule", TaskName: "Rule", DataType: "kline", Enabled: true}))
	oldRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:old", "scheduled", "1m", time.Date(2026, 8, 5, 2, 2, 0, 0, time.UTC))
	require.NoError(t, err)
	newRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", "scheduled:new", "scheduled", "1m", time.Date(2026, 8, 5, 2, 3, 0, 0, time.UTC))
	require.NoError(t, err)
	instances := []domain.TaskInstance{
		{SpaceID: "crypto", InstanceID: "old-btc", RunID: oldRun.RunID, RequestKey: "same-request", CollectionTaskID: "rule", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`},
		{SpaceID: "crypto", InstanceID: "new-btc", RunID: newRun.RunID, RequestKey: "same-request", CollectionTaskID: "rule", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`},
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, instances))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-old", SpaceID: "crypto", InstanceID: "old-btc", TaskID: "rule", DatasetID: "bars", Status: "pending"},
		{ID: "target-new", SpaceID: "crypto", InstanceID: "new-btc", TaskID: "rule", DatasetID: "bars", Status: "pending"},
	}))
	require.NoError(t, db.TaskInstances().AssignMarketFetchFunction(ctx, "crypto", "binance", "spot", "bars", "1m", "fetcher-1", []string{"BTC-USDT"}))
	at := time.Date(2026, 8, 5, 2, 3, 4, 0, time.UTC)
	delivery := &events.EventDelivery{
		Message: &eventpb.EventMessage{OccurredAt: timestamppb.New(at)},
		Payload: &storagepb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "bars", WriteSource: "scf:fetcher-1", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339)}}}}}},
	}
	require.NoError(t, handleStorageWrite(ctx, db.TaskInstances(), delivery))
	oldStored, err := db.TaskInstances().Get(ctx, "crypto", "old-btc")
	require.NoError(t, err)
	newStored, err := db.TaskInstances().Get(ctx, "crypto", "new-btc")
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusPending, oldStored.LastExecStatus)
	require.Nil(t, oldStored.LastExecTime)
	require.Empty(t, oldStored.FunctionName)
	require.Equal(t, domain.InstanceStatusSuccess, newStored.LastExecStatus)
	require.NotNil(t, newStored.LastExecTime)
	require.Equal(t, "fetcher-1", newStored.FunctionName)
}

func TestStorageWriteConsumerKeepsFullMarketFetchWindow(t *testing.T) {
	if storageWriteConsumerFetchBatch < 64 || storageWriteConsumerMaxAckPending < 512 {
		t.Fatalf("storage write consumer window fetch=%d ack=%d is too small for full-market 1m streams", storageWriteConsumerFetchBatch, storageWriteConsumerMaxAckPending)
	}
}

func TestFunctionNameFromWriteSourceRequiresSCFPrefixedSource(t *testing.T) {
	if got := functionNameFromWriteSource("scf:fetcher-1"); got != "fetcher-1" {
		t.Fatalf("functionNameFromWriteSource() = %q, want fetcher-1", got)
	}
	if got := functionNameFromWriteSource("fetcher-1"); got != "" {
		t.Fatalf("functionNameFromWriteSource(unprefixed) = %q, want empty", got)
	}
}

func newTestMarketFetchStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: t.TempDir() + "/collector.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	return db
}
