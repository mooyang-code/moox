package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

type fakeCollectorTaskResultInventoryClient struct {
	responses      []*collectorpb.GetTaskResultInventoryRsp
	requests       []*collectorpb.GetTaskResultInventoryReq
	requestSpaces  []string
	metadataSpaces []string
	err            error
}

func (c *fakeCollectorTaskResultInventoryClient) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq, _ ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error) {
	c.requests = append(c.requests, req)
	c.requestSpaces = append(c.requestSpaces, req.GetSpaceId())
	if message := codec.Message(ctx); message != nil {
		c.metadataSpaces = append(c.metadataSpaces, string(message.ClientMetaData()["space_id"]))
	}
	if c.err != nil {
		return nil, c.err
	}
	index := len(c.requests) - 1
	if index >= len(c.responses) {
		return nil, errors.New("unexpected inventory page")
	}
	return c.responses[index], nil
}

func inventoryPage(page, size, total uint32, hasMore bool, snapshot, observed string, entries ...*collectorpb.TaskResultInventoryEntry) *collectorpb.GetTaskResultInventoryRsp {
	return &collectorpb.GetTaskResultInventoryRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Entries: entries,
		Page:       &commonpb.PageResult{Page: page, Size: size, Total: total, HasMore: hasMore},
		SnapshotId: snapshot, ObservedAt: observed,
	}
}

func TestCollectorTaskResultInventorySourceReadsOneBoundedSnapshot(t *testing.T) {
	observed := "2026-10-03T10:00:00Z"
	client := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryPage(1, 1, 2, true, "snapshot-1", observed, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "crypto", TaskId: "task-a", DatasetId: "dataset-a", ViewId: "view-a", Frequency: "1m",
			ObservedAt: observed, Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
		}),
		inventoryPage(2, 1, 2, false, "snapshot-1", observed, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "crypto", TaskId: "task-b", DatasetId: "dataset-b", ViewId: "view-b", Frequency: "5m",
			ObservedAt: observed, Enabled: false, ResultStatus: "pending",
		}),
	}}
	source, err := NewCollectorTaskResultInventorySource(client, []string{" crypto "}, 1, 2)
	require.NoError(t, err)
	snapshot, err := source.FetchTaskResultInventory(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.ID)
	require.Len(t, snapshot.Entries, 2)
	require.True(t, snapshot.Entries[0].OwnershipVerified)
	require.Equal(t, "view-a", snapshot.Entries[0].ViewID)
	require.False(t, snapshot.Entries[1].Enabled)
	require.Equal(t, uint32(2), client.requests[1].GetPage().GetPage())
	require.Equal(t, "snapshot-1", client.requests[1].GetSnapshotId())
	require.Equal(t, []string{"crypto", "crypto"}, client.requestSpaces)
	require.Equal(t, []string{"crypto", "crypto"}, client.metadataSpaces, "Admin Gateway space metadata must match the request body")
}

func TestContextWithInventorySpaceIDMatchesBodyAndPreservesOtherMetadata(t *testing.T) {
	ctx, message := codec.EnsureMessage(context.Background())
	message.WithServerMetaData(codec.MetaData{"trace_id": []byte("trace-1"), "space_id": []byte("old-space")})

	scopedContext := contextWithInventorySpaceID(ctx, "stockcn")
	got := codec.Message(scopedContext).ClientMetaData()
	require.Equal(t, "stockcn", string(got["space_id"]))
	require.Equal(t, "trace-1", string(got["trace_id"]))
	require.Equal(t, "old-space", string(codec.Message(ctx).ServerMetaData()["space_id"]), "per-call Space scope must not mutate the caller context")
}

func TestCollectorTaskResultInventorySourceReadsConfiguredSpacesIndependently(t *testing.T) {
	observedCrypto := "2026-10-03T10:00:00Z"
	observedStock := "2026-10-03T10:00:05Z"
	client := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryPage(1, 1, 1, false, "crypto-snapshot", observedCrypto, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "crypto", TaskId: "crypto-task", DatasetId: "crypto-dataset", ViewId: "crypto-view", Frequency: "1m", ObservedAt: observedCrypto,
		}),
		inventoryPage(1, 1, 2, true, "stock-snapshot", observedStock, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "stockcn", TaskId: "stock-task-a", DatasetId: "stock-dataset-a", ViewId: "stock-view-a", Frequency: "1d", ObservedAt: observedStock,
		}),
		inventoryPage(2, 1, 2, false, "stock-snapshot", observedStock, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "stockcn", TaskId: "stock-task-b", DatasetId: "stock-dataset-b", ViewId: "stock-view-b", Frequency: "1d", ObservedAt: observedStock,
		}),
	}}
	source, err := NewCollectorTaskResultInventorySource(client, []string{"crypto", "stockcn"}, 1, 3)
	require.NoError(t, err)
	snapshot, err := source.FetchTaskResultInventory(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.ID, "combined identity must bind both per-Space snapshot IDs")
	require.NotEqual(t, "crypto-snapshot", snapshot.ID)
	require.Equal(t, mustParseInventoryTime(t, observedCrypto), snapshot.ObservedAt, "combined observation time is the oldest constituent snapshot")
	require.Equal(t, []string{"crypto", "stockcn", "stockcn"}, client.requestSpaces)
	require.Equal(t, []string{"crypto", "stockcn", "stockcn"}, client.metadataSpaces)
	require.Equal(t, []string{"crypto-task", "stock-task-a", "stock-task-b"}, []string{
		snapshot.Entries[0].TaskID, snapshot.Entries[1].TaskID, snapshot.Entries[2].TaskID,
	})
}

func TestCollectorTaskResultInventorySourceRejectsMixedIncompleteAndOversizedSnapshots(t *testing.T) {
	observed := "2026-10-03T10:00:00Z"
	entry := &collectorpb.TaskResultInventoryEntry{SpaceId: "crypto", TaskId: "task-a", DatasetId: "dataset-a", ViewId: "view-a", Frequency: "1m", ObservedAt: observed}
	tests := []struct {
		name      string
		maxItems  int
		responses []*collectorpb.GetTaskResultInventoryRsp
		wantError string
	}{
		{name: "mixed generation", maxItems: 2, responses: []*collectorpb.GetTaskResultInventoryRsp{
			inventoryPage(1, 1, 2, true, "snapshot-1", observed, entry),
			inventoryPage(2, 1, 2, false, "snapshot-2", observed, entry),
		}, wantError: "snapshot changed"},
		{name: "incomplete", maxItems: 2, responses: []*collectorpb.GetTaskResultInventoryRsp{
			inventoryPage(1, 1, 2, false, "snapshot-1", observed, entry),
		}, wantError: "incomplete"},
		{name: "oversized", maxItems: 1, responses: []*collectorpb.GetTaskResultInventoryRsp{
			inventoryPage(1, 1, 2, true, "snapshot-1", observed, entry),
		}, wantError: "exceeds limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeCollectorTaskResultInventoryClient{responses: test.responses}
			source, err := NewCollectorTaskResultInventorySource(client, []string{"crypto"}, 1, test.maxItems)
			require.NoError(t, err)
			_, err = source.FetchTaskResultInventory(context.Background())
			require.ErrorContains(t, err, test.wantError)
		})
	}

	wrongSpaceClient := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryPage(1, 1, 1, false, "snapshot-1", observed, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "stockcn", TaskId: "task-a", DatasetId: "dataset-a", ViewId: "view-a", Frequency: "1m", ObservedAt: observed,
		}),
	}}
	wrongSpaceSource, err := NewCollectorTaskResultInventorySource(wrongSpaceClient, []string{"crypto"}, 1, 2)
	require.NoError(t, err)
	_, err = wrongSpaceSource.FetchTaskResultInventory(context.Background())
	require.ErrorContains(t, err, "returned task from Space")

	for _, spaces := range [][]string{nil, {""}, {"crypto", " crypto "}} {
		_, err := NewCollectorTaskResultInventorySource(&fakeCollectorTaskResultInventoryClient{}, spaces, 1, 2)
		require.Error(t, err)
	}
}

func mustParseInventoryTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)
	return parsed.UTC()
}

type fakeTaskResultInventoryProvider struct {
	snapshot TaskResultInventorySnapshot
	err      error
	calls    int
}

type blockingTaskResultInventoryProvider struct {
	snapshot TaskResultInventorySnapshot
	entered  chan struct{}
	release  chan struct{}
}

func (p *blockingTaskResultInventoryProvider) FetchTaskResultInventory(context.Context) (TaskResultInventorySnapshot, error) {
	close(p.entered)
	<-p.release
	return p.snapshot, nil
}

func (p *fakeTaskResultInventoryProvider) FetchTaskResultInventory(context.Context) (TaskResultInventorySnapshot, error) {
	p.calls++
	if p.err != nil {
		return TaskResultInventorySnapshot{}, p.err
	}
	return p.snapshot, nil
}

func TestTaskResultInventoryCacheRefreshesBoundedlyAndReplacesWholeSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{
			{SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", ViewID: "view-a", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "task-disabled", DatasetID: "dataset-disabled", ViewID: "view-disabled", Frequency: "1m", Enabled: false, ObservedAt: now},
		},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)

	first, err := cache.Get(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, first.Entries, 2)
	require.Equal(t, "view-a", first.Entries[0].ViewID)
	first.Entries[0].Sessions = append(first.Entries[0].Sessions, "mutated")

	provider.snapshot = TaskResultInventorySnapshot{
		ID: "snapshot-2", ObservedAt: now.Add(2 * time.Minute),
		Entries: []TaskResultInventoryEntry{
			{SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a2", ViewID: "view-a2", Frequency: "5m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now.Add(2 * time.Minute)},
		},
	}
	cached, err := cache.Get(context.Background(), now.Add(time.Minute-time.Second))
	require.NoError(t, err)
	require.Equal(t, "snapshot-1", cached.ID)
	require.Empty(t, cached.Entries[0].Sessions)
	require.Equal(t, 1, provider.calls)

	refreshed, err := cache.Get(context.Background(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "snapshot-2", refreshed.ID)
	require.Len(t, refreshed.Entries, 1) // disabled and deleted tasks disappear atomically
	require.Equal(t, "view-a2", refreshed.Entries[0].ViewID)
	require.Equal(t, 2, provider.calls)
}

func TestTaskResultInventoryCacheDoesNotServeExpiredPartialOrOversizedState(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{SpaceID: "crypto", TaskID: "a", DatasetID: "d", ViewID: "v", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 1)
	require.NoError(t, err)
	_, err = cache.Get(context.Background(), now)
	require.NoError(t, err)

	provider.err = errors.New("collector unavailable")
	_, err = cache.Get(context.Background(), now.Add(time.Minute))
	require.ErrorContains(t, err, "collector unavailable")
	state := cache.State(now.Add(time.Minute))
	require.Equal(t, now, state.LastSuccess)
	require.Equal(t, now.Add(time.Minute), state.LastAttempt)
	require.Equal(t, time.Minute, state.Age)
	require.False(t, state.Available)
	require.Contains(t, state.LastError, "collector unavailable")

	provider.err = nil
	provider.snapshot = TaskResultInventorySnapshot{
		ID: "snapshot-oversized", ObservedAt: now.Add(2 * time.Minute),
		Entries: []TaskResultInventoryEntry{
			{SpaceID: "crypto", TaskID: "a", DatasetID: "d", ViewID: "v", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "b", DatasetID: "d2", ViewID: "v2", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
		},
	}
	_, err = cache.Get(context.Background(), now.Add(2*time.Minute))
	require.ErrorContains(t, err, "exceeds limit")
}

func TestTaskResultInventoryRefreshMetricsExposeLowCardinalityOutcomes(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{ID: "snapshot-metrics", ObservedAt: now}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	_, err = cache.Get(context.Background(), now)
	require.NoError(t, err)
	provider.err = errors.New("collector unavailable")
	_, err = cache.Get(context.Background(), now.Add(time.Minute))
	require.Error(t, err)

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	found := make(map[string]bool)
	for _, family := range families {
		switch family.GetName() {
		case "moox_monitor_task_result_inventory_refresh_total":
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "result" && (label.GetValue() == "success" || label.GetValue() == "error") {
						found[label.GetValue()] = true
					}
				}
			}
		case "moox_monitor_task_result_inventory_last_success_timestamp_seconds", "moox_monitor_task_result_inventory_cache_age_seconds", "moox_monitor_task_result_inventory_cache_available":
			found[family.GetName()] = true
		}
	}
	require.True(t, found["success"])
	require.True(t, found["error"])
	require.True(t, found["moox_monitor_task_result_inventory_last_success_timestamp_seconds"])
	require.True(t, found["moox_monitor_task_result_inventory_cache_age_seconds"])
	require.True(t, found["moox_monitor_task_result_inventory_cache_available"])
}

func TestTaskResultInventoryCurrentDoesNotWaitForRefreshNetworkCall(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &blockingTaskResultInventoryProvider{
		snapshot: TaskResultInventorySnapshot{ID: "snapshot-1", ObservedAt: now},
		entered:  make(chan struct{}), release: make(chan struct{}),
	}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() {
		_, err := cache.Get(context.Background(), now)
		refreshDone <- err
	}()
	<-provider.entered

	currentDone := make(chan bool, 1)
	go func() {
		_, ok := cache.Current(now.Add(time.Minute))
		currentDone <- ok
	}()
	select {
	case ok := <-currentDone:
		require.False(t, ok)
	case <-time.After(100 * time.Millisecond):
		close(provider.release)
		<-refreshDone
		t.Fatal("Current blocked behind inventory network refresh")
	}
	close(provider.release)
	require.NoError(t, <-refreshDone)
}

func TestKlineFreshnessEvaluatorUsesDynamicTaskInventoryAndReportsStaleAge(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-private", DatasetID: "dataset-dynamic", ViewID: "view-dynamic", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
			LatestCompletedPeriod: now.Add(-time.Minute), LatestCompletedStatus: "complete",
			ViewLastDataTime: now.Add(-time.Minute), ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewDatasetQuery(t, []viewDatasetTestSample{{
		SpaceID: "crypto", ViewID: "view-dynamic", DatasetID: "dataset-dynamic", Subject: "BTC-USDT",
		Output: now.Add(-3 * time.Minute), Commit: now,
	}})
	evaluator := NewKlineFreshnessEvaluatorWithInventory(query, cache, 2*time.Minute, 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	var klineReport *KlineFreshnessReport
	for index := range reports {
		if reports[index].CheckID == "kline_freshness:crypto:view-dynamic:1m" {
			klineReport = &reports[index]
		}
	}
	require.NotNil(t, klineReport)
	require.False(t, klineReport.Success)
	require.Equal(t, "business_data_stale", klineReport.Reason)
	require.NotContains(t, klineReport.CheckID, "task-private")
	require.Equal(t, 3*time.Minute, klineReport.StaleAge)
	require.Equal(t, now.Add(-time.Minute), klineReport.Rule.LatestCompletedPeriod)
	require.Equal(t, now.Add(-time.Minute), klineReport.Rule.ViewLastDataTime)
	require.Contains(t, klineReport.Diagnostic, "latest_completed_period=")
	require.Contains(t, klineReport.Diagnostic, "view_last_data_time=")
}

func TestKlineFreshnessEvaluatorDetectsViewBehindCompletedPeriodWithinStaleWindow(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-private", DatasetID: "dataset-dynamic", ViewID: "view-dynamic", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
			LatestCompletedPeriod: now.Add(-time.Minute), LatestCompletedStatus: "complete",
			ViewLastDataTime: now.Add(-2 * time.Minute), ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewDatasetQuery(t, []viewDatasetTestSample{{
		SpaceID: "crypto", ViewID: "view-dynamic", DatasetID: "dataset-dynamic", Subject: "BTC-USDT",
		Output: now.Add(-2 * time.Minute), Commit: now,
	}})
	evaluator := NewKlineFreshnessEvaluatorWithInventory(query, cache, 10*time.Minute, 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	var klineReport *KlineFreshnessReport
	for index := range reports {
		if reports[index].CheckID == "kline_freshness:crypto:view-dynamic:1m" {
			klineReport = &reports[index]
		}
	}
	require.NotNil(t, klineReport)
	require.False(t, klineReport.Success)
	require.Equal(t, "view_behind_latest_completed_period", klineReport.Reason)
	require.Equal(t, time.Minute, klineReport.StaleAge)
	require.Contains(t, klineReport.Diagnostic, "latest_completed_period=")
}

func TestKlineFreshnessEvaluatorIgnoresShortPendingTaskBacklog(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-pending", DatasetID: "dataset-pending", ViewID: "view-pending", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: false, ResultStatus: "pending", ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewDatasetQuery(t, nil)
	reports, err := NewKlineFreshnessEvaluatorWithInventory(query, cache, 2*time.Minute, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	require.Equal(t, TaskResultInventoryCheckID, reports[1].CheckID)
	require.True(t, reports[1].Success)
}

func TestKlineFreshnessEvaluatorPreservesUnownedErrorAndPendingTaskStates(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-statuses", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{
			{SpaceID: "crypto", TaskID: "task-error", DatasetID: "dataset-error", ViewID: "view-error", Frequency: "1m", Enabled: true, ResultStatus: "error", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "task-pending", DatasetID: "dataset-pending", ViewID: "view-pending", Frequency: "1m", Enabled: true, ResultStatus: "pending", ObservedAt: now},
		},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewDatasetQuery(t, nil)
	reports, err := NewKlineFreshnessEvaluatorWithInventory(query, cache, 2*time.Minute, 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 3)

	byID := make(map[string]KlineFreshnessReport, len(reports))
	for _, report := range reports {
		byID[report.CheckID] = report
	}
	inventoryReport, ok := byID[TaskResultInventoryCheckID]
	require.True(t, ok)
	require.True(t, inventoryReport.Success)
	errorReport, ok := byID["kline_freshness:crypto:view-error:1m"]
	require.True(t, ok)
	require.False(t, errorReport.Success)
	require.Equal(t, "task_result_error", errorReport.Reason)
	pendingReport, ok := byID["kline_freshness:crypto:view-pending:1m"]
	require.True(t, ok)
	require.True(t, pendingReport.Skipped)
	require.Equal(t, "task_result_not_ready", pendingReport.Reason)
}
