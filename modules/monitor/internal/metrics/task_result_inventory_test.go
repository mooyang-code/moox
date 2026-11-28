package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/report"
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

func inventoryResponse(observed int64, entries ...*collectorpb.TaskResultInventoryEntry) *collectorpb.GetTaskResultInventoryRsp {
	return &collectorpb.GetTaskResultInventoryRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Entries: entries,
		ObservedAt: observed,
	}
}

func TestCollectorTaskResultInventorySourceReadsOneBoundedSnapshot(t *testing.T) {
	observed := mustParseInventoryTime(t, "2026-10-03T10:00:00Z").UnixMilli()
	client := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryResponse(observed,
			&collectorpb.TaskResultInventoryEntry{
				SpaceId: "crypto", TaskId: "task-a", DatasetId: "dataset-a", Frequency: "1m",
				ObservedAt: observed, Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
			},
			&collectorpb.TaskResultInventoryEntry{
				SpaceId: "crypto", TaskId: "task-b", DatasetId: "dataset-b", Frequency: "5m",
				ObservedAt: observed, Enabled: false, ResultStatus: "pending",
			},
		),
	}}
	source, err := NewCollectorTaskResultInventorySource(client, []string{" crypto "}, 100, 2)
	require.NoError(t, err)
	snapshot, err := source.FetchTaskResultInventory(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.ID)
	require.Len(t, snapshot.Entries, 2)
	require.True(t, snapshot.Entries[0].OwnershipVerified)
	require.Equal(t, "dataset-a", snapshot.Entries[0].DatasetID)
	require.False(t, snapshot.Entries[1].Enabled)
	require.Equal(t, []string{"crypto"}, client.requestSpaces)
	require.Equal(t, []string{"crypto"}, client.metadataSpaces, "Admin Gateway space metadata must match the request body")
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
	observedCrypto := mustParseInventoryTime(t, "2026-10-03T10:00:00Z").UnixMilli()
	observedStock := mustParseInventoryTime(t, "2026-10-03T10:00:05Z").UnixMilli()
	client := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryResponse(observedCrypto, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "crypto", TaskId: "crypto-task", DatasetId: "crypto-dataset", Frequency: "1m", ObservedAt: observedCrypto,
		}),
		inventoryResponse(observedStock,
			&collectorpb.TaskResultInventoryEntry{
				SpaceId: "stockcn", TaskId: "stock-task-a", DatasetId: "stock-dataset-a", Frequency: "1d", ObservedAt: observedStock,
			},
			&collectorpb.TaskResultInventoryEntry{
				SpaceId: "stockcn", TaskId: "stock-task-b", DatasetId: "stock-dataset-b", Frequency: "1d", ObservedAt: observedStock,
			},
		),
	}}
	source, err := NewCollectorTaskResultInventorySource(client, []string{"crypto", "stockcn"}, 100, 3)
	require.NoError(t, err)
	snapshot, err := source.FetchTaskResultInventory(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.ID, "combined identity must bind per-Space inventories")
	require.Equal(t, time.UnixMilli(observedCrypto).UTC(), snapshot.ObservedAt, "combined observation time is the oldest constituent snapshot")
	require.Equal(t, []string{"crypto", "stockcn"}, client.requestSpaces)
	require.Equal(t, []string{"crypto", "stockcn"}, client.metadataSpaces)
	require.Equal(t, []string{"crypto-task", "stock-task-a", "stock-task-b"}, []string{
		snapshot.Entries[0].TaskID, snapshot.Entries[1].TaskID, snapshot.Entries[2].TaskID,
	})
}

func TestCollectorTaskResultInventorySourceRejectsOversizedAndCrossSpaceSnapshots(t *testing.T) {
	observed := mustParseInventoryTime(t, "2026-10-03T10:00:00Z").UnixMilli()
	entry := &collectorpb.TaskResultInventoryEntry{SpaceId: "crypto", TaskId: "task-a", DatasetId: "dataset-a", Frequency: "1m", ObservedAt: observed}
	oversizedClient := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryResponse(observed, entry, &collectorpb.TaskResultInventoryEntry{SpaceId: "crypto", TaskId: "task-b", DatasetId: "dataset-b", Frequency: "1m", ObservedAt: observed}),
	}}
	oversizedSource, err := NewCollectorTaskResultInventorySource(oversizedClient, []string{"crypto"}, 100, 1)
	require.NoError(t, err)
	_, err = oversizedSource.FetchTaskResultInventory(context.Background())
	require.ErrorContains(t, err, "exceeds limit")

	wrongSpaceClient := &fakeCollectorTaskResultInventoryClient{responses: []*collectorpb.GetTaskResultInventoryRsp{
		inventoryResponse(observed, &collectorpb.TaskResultInventoryEntry{
			SpaceId: "stockcn", TaskId: "task-a", DatasetId: "dataset-a", Frequency: "1m", ObservedAt: observed,
		}),
	}}
	wrongSpaceSource, err := NewCollectorTaskResultInventorySource(wrongSpaceClient, []string{"crypto"}, 100, 2)
	require.NoError(t, err)
	_, err = wrongSpaceSource.FetchTaskResultInventory(context.Background())
	require.ErrorContains(t, err, "returned task from Space")

	for _, spaces := range [][]string{nil, {""}, {"crypto", " crypto "}} {
		_, err := NewCollectorTaskResultInventorySource(&fakeCollectorTaskResultInventoryClient{}, spaces, 100, 2)
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
			{SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "task-disabled", DatasetID: "dataset-disabled", Frequency: "1m", Enabled: false, ObservedAt: now},
		},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)

	first, err := cache.Get(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, first.Entries, 2)
	require.Equal(t, "dataset-a", first.Entries[0].DatasetID)
	first.Entries[0].Sessions = append(first.Entries[0].Sessions, "mutated")

	provider.snapshot = TaskResultInventorySnapshot{
		ID: "snapshot-2", ObservedAt: now.Add(2 * time.Minute),
		Entries: []TaskResultInventoryEntry{
			{SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a2", Frequency: "5m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now.Add(2 * time.Minute)},
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
	require.Equal(t, "dataset-a2", refreshed.Entries[0].DatasetID)
	require.Equal(t, 2, provider.calls)
}

func TestTaskResultInventoryCacheDoesNotServeExpiredPartialOrOversizedState(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{SpaceID: "crypto", TaskID: "a", DatasetID: "d", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now}},
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
			{SpaceID: "crypto", TaskID: "a", DatasetID: "d", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "b", DatasetID: "d2", Frequency: "1m", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now},
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

func TestKlineFreshnessEvaluatorUsesDynamicTaskInventoryAndReportsStaleAge(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
		ID: "snapshot-1", ObservedAt: now,
		Entries: []TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-private", DatasetID: "dataset-dynamic", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
			LatestCompletedPeriod: now.Add(-time.Minute), LatestCompletedStatus: "complete",
			ViewLastDataTime: now.Add(-time.Minute), ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		DatasetID: "dataset-dynamic", Subjects: 1, ObservedAt: now,
		// After the 1m bar starting five minutes ago, the next bar was due three
		// minutes ago.
		Latest: now.Add(-5 * time.Minute),
	}})
	evaluator := NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(2*time.Minute), 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	var klineReport *KlineFreshnessReport
	for index := range reports {
		if reports[index].CheckID == "kline_freshness:crypto:dataset-dynamic:1m" {
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
			SpaceID: "crypto", TaskID: "task-private", DatasetID: "dataset-dynamic", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
			LatestCompletedPeriod: now.Add(-2 * time.Minute), LatestCompletedStatus: "complete",
			ViewLastDataTime: now.Add(-3 * time.Minute), ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	// 采集已完成 now-2m 那一期（now-1m 收线），视图只到 now-3m：缺的那根已过收线 1 分钟。
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{
		DatasetID: "dataset-dynamic", Subjects: 1, ObservedAt: now,
		Latest: now.Add(-3 * time.Minute),
	}})
	evaluator := NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(10*time.Minute), 20)
	reports, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 2)
	var klineReport *KlineFreshnessReport
	for index := range reports {
		if reports[index].CheckID == "kline_freshness:crypto:dataset-dynamic:1m" {
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
			SpaceID: "crypto", TaskID: "task-pending", DatasetID: "dataset-pending", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: false, ResultStatus: "pending", ObservedAt: now,
		}},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewSummaryQuery(t, nil)
	reports, err := NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(2*time.Minute), 20).Evaluate(context.Background(), now)
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
			{SpaceID: "crypto", TaskID: "task-error", DatasetID: "dataset-error", Frequency: "1m", Enabled: true, ResultStatus: "error", ObservedAt: now},
			{SpaceID: "crypto", TaskID: "task-pending", DatasetID: "dataset-pending", Frequency: "1m", Enabled: true, ResultStatus: "pending", ObservedAt: now},
		},
	}}
	cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	query := newViewSummaryQuery(t, nil)
	reports, err := NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(2*time.Minute), 20).Evaluate(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, reports, 3)

	byID := make(map[string]KlineFreshnessReport, len(reports))
	for _, report := range reports {
		byID[report.CheckID] = report
	}
	inventoryReport, ok := byID[TaskResultInventoryCheckID]
	require.True(t, ok)
	require.True(t, inventoryReport.Success)
	errorReport, ok := byID["kline_freshness:crypto:dataset-error:1m"]
	require.True(t, ok)
	require.False(t, errorReport.Success)
	require.Equal(t, "task_result_error", errorReport.Reason)
	pendingReport, ok := byID["kline_freshness:crypto:dataset-pending:1m"]
	require.True(t, ok)
	require.True(t, pendingReport.Skipped)
	require.Equal(t, "task_result_not_ready", pendingReport.Reason)
}

// latenessPolicy 是所有周期都允许迟到 lateness 的健康策略。
func latenessPolicy(lateness time.Duration) report.RealtimeTimeSeriesPolicy {
	return report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
		RunMissedIntervals: 2, SuccessMissedIntervals: 3, MinuteLateness: lateness, HourLateness: lateness,
	}}
}

// 首次接入的历史补齐中（或刚补齐）：补齐写入的历史行排在变更日志前面，结果视图落后于已完成的一期是预期的，
// 采集侧已完成的一期没过期就按通过记；采集侧也过期（采集真停了）照样报视图落后；不在补齐中的空间照常报。
func TestKlineFreshnessEvaluatorToleratesViewCatchingUpWhileSeeding(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	evaluate := func(completed time.Time, seeding bool) KlineFreshnessReport {
		provider := &fakeTaskResultInventoryProvider{snapshot: TaskResultInventorySnapshot{
			ID: "snapshot-1", ObservedAt: now,
			Entries: []TaskResultInventoryEntry{{
				SpaceID: "crypto", TaskID: "task", DatasetID: "dataset-seed", Frequency: "1m",
				MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready",
				LatestCompletedPeriod: completed, LatestCompletedStatus: "complete",
				ViewLastDataTime: now.Add(-3 * time.Hour), ObservedAt: now,
			}},
		}}
		cache, err := NewTaskResultInventoryCache(provider, time.Minute, 10)
		require.NoError(t, err)
		query := newViewSummaryQuery(t, []viewSummaryTestSample{{DatasetID: "dataset-seed", Subjects: 1, ObservedAt: now, Latest: now.Add(-3 * time.Hour)}})
		evaluator := NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(30*time.Second), 20)
		evaluator.SetSeeding(func(spaceID string, _ time.Time) bool { return seeding && spaceID == "crypto" })
		reports, err := evaluator.Evaluate(context.Background(), now)
		require.NoError(t, err)
		for _, report := range reports {
			if report.CheckID == "kline_freshness:crypto:dataset-seed:1m" {
				return report
			}
		}
		t.Fatal("没有 K 线检查")
		return KlineFreshnessReport{}
	}
	fresh := now.Add(-time.Minute) // now-1m 那一根 now 收线：采集按时
	report := evaluate(fresh, true)
	require.True(t, report.Success)
	require.Equal(t, KlineReasonSeedingViewCatchingUp, report.Reason)
	require.Equal(t, now.Add(-3*time.Hour), report.OldestDataTime)

	stopped := now.Add(-10 * time.Minute) // 采集侧停在 10 分钟前
	report = evaluate(stopped, true)
	require.False(t, report.Success, "采集真停了照样报")
	require.Equal(t, "view_behind_latest_completed_period", report.Reason)

	report = evaluate(fresh, false)
	require.False(t, report.Success, "不在补齐中的空间照常报")
	require.Equal(t, "view_behind_latest_completed_period", report.Reason)
}

// 清单里的历史补齐状态原样带到 K 线新鲜度规则上；结束时刻为 0 表示没有补齐记录或还在补齐。
func TestTaskResultInventoryCarriesHistorySeedToRules(t *testing.T) {
	observed := time.Date(2026, 11, 25, 9, 50, 0, 0, time.UTC)
	finished := observed.Add(-5 * time.Minute)
	seeding, err := taskResultInventoryEntryFromProto(&collectorpb.TaskResultInventoryEntry{
		SpaceId: "stockus", TaskId: "task-us", DatasetId: "ds-us", Frequency: "5m", ObservedAt: observed.UnixMilli(), HistorySeeding: true,
	})
	require.NoError(t, err)
	require.True(t, seeding.HistorySeeding)
	require.True(t, seeding.HistorySeedFinishedAt.IsZero())
	seeded, err := taskResultInventoryEntryFromProto(&collectorpb.TaskResultInventoryEntry{
		SpaceId: "stockcn", TaskId: "task-cn", DatasetId: "ds-cn", Frequency: "5m", ObservedAt: observed.UnixMilli(), HistorySeedFinishedAt: finished.UnixMilli(),
	})
	require.NoError(t, err)
	require.False(t, seeded.HistorySeeding)
	require.Equal(t, finished, seeded.HistorySeedFinishedAt)

	seeding.Enabled, seeding.OwnershipVerified, seeding.ResultStatus = true, true, "ready"
	seeded.Enabled, seeded.OwnershipVerified, seeded.ResultStatus = true, true, "ready"
	rules := klineFreshnessRulesFromInventory(TaskResultInventorySnapshot{ID: "s", ObservedAt: observed, Entries: []TaskResultInventoryEntry{seeding, seeded}}, latenessPolicy(time.Minute))
	require.Len(t, rules, 2)
	require.True(t, rules[0].HistorySeeding)
	require.Equal(t, finished, rules[1].HistorySeedFinishedAt)
	require.True(t, rules[1].seedingSubjects(observed), "结束 5 分钟内")
	require.False(t, rules[1].seedingSubjects(finished.Add(31*time.Minute)))
}
