package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type selectiveTargetStorage struct {
	mu     sync.Mutex
	writes map[string]int
	failOn map[string]int
}

func newSelectiveTargetStorage() *selectiveTargetStorage {
	return &selectiveTargetStorage{writes: map[string]int{}, failOn: map[string]int{"bars-b": 1}}
}

func (s *selectiveTargetStorage) UpsertFields(ctx context.Context, rows []*storagepb.RowFieldUpsert) error {
	return s.UpsertFieldsWithSource(ctx, rows, "")
}

func (s *selectiveTargetStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, _ string) error {
	if len(rows) == 0 || rows[0].GetKey() == nil {
		return nil
	}
	datasetID := rows[0].GetKey().GetDatasetId()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes[datasetID]++
	if s.failOn[datasetID] == s.writes[datasetID] {
		return fmt.Errorf("injected write failure for %s", datasetID)
	}
	return nil
}

func (s *selectiveTargetStorage) count(datasetID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes[datasetID]
}

func TestSharedInstancePartialTargetFailureRetriesOnlyFailedDestinationE2E(t *testing.T) {
	ctx := context.Background()
	db := newTestMarketFetchStore(t)
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "Target A", DataType: "kline", Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "Target B", DataType: "kline", Enabled: true},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}

	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared-btc", Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	targets := []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-a", DatasetID: "bars-a", OutputFields: `["close"]`, Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-b", DatasetID: "bars-b", OutputFields: `["close"]`, Status: "pending"},
	}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, targets))

	barStart := time.Date(2026, 9, 28, 5, 59, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", SourceID: "binance", ProviderSymbol: "BTCUSDT", Frequency: "1m", BarStart: barStart, BarEnd: barStart.Add(time.Minute), Open: 100, High: 101, Low: 99, Close: 100.5, VolumeShares: 10, AmountCNY: 1005, ProviderTimestamp: barStart.Add(time.Minute), FetchedAt: barStart.Add(2 * time.Minute), RequestID: "shared-target-e2e"}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 10, pipelineClock{now: barStart.Add(2 * time.Minute)}, nil)
	require.NoError(t, err)
	storage := newSelectiveTargetStorage()
	pipeline := &KlinePipeline{Router: router, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", Now: func() time.Time { return barStart.Add(2 * time.Minute) }}

	initialReq := Request{
		BatchID: "batch-initial", SyncPointID: "sync-initial", ScheduleID: "schedule-initial", BatchKind: domain.BatchKindBackfill,
		SpaceID: "crypto", DatasetID: "bars-a", Frequency: "1m", Provider: "binance", SourceID: "binance", MarketType: "spot", NodeID: "invoke-1", FunctionName: "fetch-1",
		Items:   []domain.CollectionItem{{InstanceID: instance.InstanceID, SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars-a", Frequency: "1m", TargetDataTime: barStart.Format(time.RFC3339Nano), StartTime: barStart.Format(time.RFC3339Nano), EndTime: barStart.Add(time.Minute).Format(time.RFC3339Nano), BarLimit: 1}},
		Targets: targets,
	}
	raw, err := json.Marshal(initialReq)
	require.NoError(t, err)
	plannedAt := barStart.Add(90 * time.Second)
	batch := &domain.BatchInvocation{SpaceID: "crypto", BatchID: initialReq.BatchID, ScheduleID: initialReq.ScheduleID, BatchKind: initialReq.BatchKind, Frequency: initialReq.Frequency, NodeID: initialReq.NodeID, FunctionName: initialReq.FunctionName, Status: domain.BatchStatusPlanned, PlannedCount: 1, RequestJSON: string(raw), PlannedAt: &plannedAt}
	created, err := db.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{instance.InstanceID}))

	firstCompletion, err := pipeline.Execute(ctx, initialReq)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstCompletion.GetStatus(), "provider fetch succeeds even when one destination write fails")
	require.Len(t, firstCompletion.GetItems(), 1)
	require.Len(t, firstCompletion.GetItems()[0].GetTargets(), 2)
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(firstCompletion)))

	targetA, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-a")
	require.NoError(t, err)
	targetB, err := db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-b")
	require.NoError(t, err)
	require.Equal(t, "succeeded", targetA.Status)
	require.Equal(t, "failed", targetB.Status)
	storedInstance, err := db.TaskInstances().Get(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusSuccess, storedInstance.LastExecStatus)

	retryKey := writeTargetRetryKey(instance.InstanceID, "target-b", barStart.Format(time.RFC3339Nano))
	retry, err := db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	require.Equal(t, "write_target", retry.RetryScope)
	require.Equal(t, "target-b", retry.WriteTargetID)

	dispatchAt := firstCompletion.GetCompletedAt().AsTime().Add(10 * time.Second)
	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, MaxRetryAttempts: 3, Now: func() time.Time { return dispatchAt }}
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, "crypto", nodes, dispatchAt))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	data, ok := invoker.snapshot()[0].event["data"].(map[string]any)
	require.True(t, ok)
	targetPayload, ok := data["targets"].([]any)
	require.True(t, ok)
	require.Len(t, targetPayload, 1)
	require.Contains(t, fmt.Sprint(targetPayload[0]), "bars-b")
	require.NotContains(t, fmt.Sprint(targetPayload[0]), "bars-a")

	retryBatchID := stableID("crypto", "retry", retryKey, "2")
	retryBatch, err := db.FetchBatches().Get(ctx, "crypto", retryBatchID)
	require.NoError(t, err)
	var retryReq Request
	require.NoError(t, json.Unmarshal([]byte(retryBatch.RequestJSON), &retryReq))
	require.Len(t, retryReq.Targets, 1)
	require.Equal(t, "bars-b", retryReq.Targets[0].DatasetID)
	olderTargetARetryKey := "older-target-a-retry"
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: olderTargetARetryKey, InstanceID: instance.InstanceID,
		WriteTargetID: "target-a", RetryScope: "write_target", SubjectID: "BTC-USDT", Frequency: "1m",
		TargetDataTime: barStart.Add(-time.Minute), Status: "pending", Attempt: 1,
	}))
	retryCompletion, err := pipeline.Execute(ctx, retryReq)
	require.NoError(t, err)
	require.Equal(t, "succeeded", retryCompletion.GetStatus())
	require.NoError(t, handleCompletion(ctx, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil, completionTestDelivery(retryCompletion)))

	targetA, err = db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-a")
	require.NoError(t, err)
	targetB, err = db.TaskInstances().GetWriteTarget(ctx, "crypto", "target-b")
	require.NoError(t, err)
	require.Equal(t, "succeeded", targetA.Status)
	require.Equal(t, "succeeded", targetB.Status)
	retry, err = db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	require.Equal(t, "succeeded", retry.Status)
	olderTargetARetry, err := db.FetchRetries().Get(ctx, "crypto", olderTargetARetryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", olderTargetARetry.Status, "success for target-b must not supersede another destination's retry")
	require.Equal(t, 1, storage.count("bars-a"), "successful sibling target must not be rewritten")
	require.Equal(t, 2, storage.count("bars-b"), "failed target gets exactly one retry write")
}
