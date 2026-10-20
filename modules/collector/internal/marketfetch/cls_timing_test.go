package marketfetch

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	require.Zero(t, percentile(nil, 90))
	values := []int64{50, 10, 40, 30, 20, 100, 90, 80, 70, 60}
	require.Equal(t, int64(50), percentile(values, 50))
	require.Equal(t, int64(90), percentile(values, 90))
	require.Equal(t, int64(100), percentile(values, 100))
	require.Equal(t, []int64{50, 10, 40, 30, 20, 100, 90, 80, 70, 60}, values, "不改调用方的切片")
}

func TestBatchTimingFieldsSummarizeStagesAndRetries(t *testing.T) {
	closeAt := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	items := []itemTiming{
		{Recorded: true, Fetch: 300 * time.Millisecond, Queue: 0, Attempts: 1, HTTP: httpclient.TraceSnapshot{Requests: 1, ReusedConns: 1, TTFBMs: 200}},
		{Recorded: true, Fetch: 4 * time.Second, Queue: 50 * time.Millisecond, Attempts: 2, HTTP: httpclient.TraceSnapshot{Requests: 2, NewConns: 2, DNSMs: 5, ConnectMs: 400, TLSMs: 700, TTFBMs: 2500}},
		{}, // 没走到取数的项不计入。
	}
	fields := batchTimingFields(items, closeAt.Add(500*time.Millisecond), closeAt.Add(4600*time.Millisecond), closeAt, 0)
	require.Equal(t, "0", fields["standby_ms"], "standby_ms 总是上报，0 表示这批没有启用备援")
	require.Equal(t, "500", fields["since_close_start_ms"])
	require.Equal(t, "4600", fields["since_close_fetch_end_ms"])
	require.Equal(t, "4000", fields["item_max_ms"])
	require.Equal(t, "300", fields["item_p50_ms"])
	require.Equal(t, "50", fields["queue_max_ms"])
	require.Equal(t, "3", fields["attempts_total"])
	require.Equal(t, "1", fields["retried"])
	require.Equal(t, "3", fields["http_requests"])
	require.Equal(t, "2", fields["conn_new"])
	require.Equal(t, "1", fields["conn_reused"])
	require.Equal(t, "5", fields["dns_max_ms"])
	require.Equal(t, "400", fields["connect_max_ms"])
	require.Equal(t, "700", fields["tls_max_ms"])
	require.Equal(t, "2500", fields["ttfb_max_ms"])
	require.NotContains(t, fields, "fail_total", "没有失败尝试时不上报失败计数")

	// 失败尝试按原因、阶段与是否复用连接计数。
	items[1].HTTP.Attempts = []httpclient.TraceAttempt{
		{Domain: "api.binance.com", Phase: "wait_response", Kind: "timeout", Ms: 1200, Reused: true, IdleMs: 40000},
		{Domain: "api.binance.com", Phase: "connect", Kind: "dial", Ms: 800},
		{Domain: "data-api.binance.vision", Phase: "done", Status: 200, Ms: 300},
	}
	fields = batchTimingFields(items, closeAt, closeAt, closeAt, 0)
	require.Equal(t, "2", fields["fail_total"])
	require.Equal(t, "1", fields["fail_timeout"])
	require.Equal(t, "1", fields["fail_dial"])
	require.Equal(t, "1", fields["fail_in_wait_response"])
	require.Equal(t, "1", fields["fail_in_connect_phase"])
	require.Equal(t, "1", fields["fail_on_reused_conn"])
	require.Equal(t, "1200", fields["fail_ms_max"])

	// 备援：上报延迟配置与触发、获胜次数。
	items[1].HTTP.StandbyFired, items[1].HTTP.StandbyWon = 1, 1
	fields = batchTimingFields(items, closeAt, closeAt, closeAt, 800)
	require.Equal(t, "800", fields["standby_ms"])
	require.Equal(t, "1", fields["standby_fired"])
	require.Equal(t, "1", fields["standby_won"])
}

func TestReportSlowItemsPicksSlowestAndRetriedWithinTheCap(t *testing.T) {
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{Reporter: reporter, SpaceID: "crypto", FunctionName: "fn"}
	req := Request{RequestID: "r", DatasetID: "ds", Frequency: "1m", BatchKind: domain.BatchKindRealtime}
	closeAt := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	var items []itemTiming
	for i := 0; i < 20; i++ {
		items = append(items, itemTiming{Recorded: true, Subject: "FAST-" + strconv.Itoa(i), Fetch: 200 * time.Millisecond, Attempts: 1, HTTP: httpclient.TraceSnapshot{Requests: 1}})
	}
	for i := 0; i < 12; i++ {
		items = append(items, itemTiming{Recorded: true, Subject: "SLOW-" + strconv.Itoa(i), Fetch: time.Duration(1000+i*100) * time.Millisecond, Attempts: 1, StartedAt: closeAt.Add(time.Second), HTTP: httpclient.TraceSnapshot{Requests: 1, TTFBMs: 900}})
	}
	items = append(items, itemTiming{Recorded: true, Subject: "RETRY", Fetch: 100 * time.Millisecond, Attempts: 2, HTTP: httpclient.TraceSnapshot{Requests: 2}})
	pipeline.reportSlowItems(req, items, closeAt)

	require.Len(t, reporter.entries, maxSlowItemReports)
	require.NotContains(t, reporter.entries[0].Fields, "attempt_trace", "没有逐次记录时不带 attempt_trace")
	first := reporter.entries[0].Fields
	require.Equal(t, "market_fetch_item_slow", first["log_type"])
	require.Equal(t, "SLOW-11", first["subject_id"], "最慢的排最前")
	require.Equal(t, "2100", first["fetch_ms"])
	require.Equal(t, "1000", first["since_close_start_ms"])
	for _, entry := range reporter.entries {
		require.NotEqual(t, "FAST-0", entry.Fields["subject_id"])
	}

	// 重试过的项即使很快也上报。
	reporter.entries = nil
	pipeline.reportSlowItems(req, items[len(items)-1:], closeAt)
	require.Len(t, reporter.entries, 1)
	require.Equal(t, "RETRY", reporter.entries[0].Fields["subject_id"])
}

func TestPeriodCloseTimeOnlyForPeriodCommitRequests(t *testing.T) {
	req := Request{RequirePeriodCommit: true, Items: []domain.CollectionItem{{TargetDataTime: "2026-10-20T05:59:00Z"}}}
	frequency := mustFrequency(t, "1m")
	require.Equal(t, time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC), periodCloseTime(req, frequency))
	req.RequirePeriodCommit = false
	require.True(t, periodCloseTime(req, frequency).IsZero())
	require.True(t, periodCloseTime(Request{RequirePeriodCommit: true}, frequency).IsZero())
}

func mustFrequency(t *testing.T, name string) marketdata.Frequency {
	t.Helper()
	frequency, err := marketdata.ParseFrequency(name)
	require.NoError(t, err)
	return frequency
}

func TestKlinePipelineReportsStageTimingsAndCommit(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 5, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "sina", SourceID: "stockcn_minute_http",
		ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "timing",
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", sourceID: "stockcn_minute_http", rows: []marketdata.NormalizedKline{bar}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{
		Router: router, Storage: &pipelineStorage{}, CandidateChain: []string{"sina"}, Reporter: reporter, FunctionName: "fn",
		SourceID: "stockcn_minute_http", MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID,
		DatasetID: StockCNDatasetID, Now: func() time.Time { return now },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = pipeline.Execute(ctx, Request{
		BatchID: "timing", BatchKind: domain.BatchKindRealtime,
		SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
		Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequestID: "timing",
		Items: []domain.CollectionItem{{
			SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", SourceID: "stockcn_minute_http",
			MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1,
		}},
	})
	require.NoError(t, err)

	byType := map[string]map[string]string{}
	for _, entry := range reporter.entries {
		byType[entry.Fields["log_type"]] = entry.Fields
	}
	batch := byType["market_fetch_batch"]
	require.NotNil(t, batch)
	require.Contains(t, batch, "item_p50_ms")
	require.Equal(t, "1", batch["attempts_total"])
	require.Equal(t, "0", batch["retried"])
	commit := byType["market_fetch_commit"]
	require.NotNil(t, commit)
	require.Equal(t, "1", commit["rows"])
	require.Contains(t, commit, "commit_ms")
	require.NotContains(t, byType, "market_fetch_item_slow", "正常的单项不单独上报")
}

func TestReportSlowItemsCarriesAttemptTrace(t *testing.T) {
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{Reporter: reporter, SpaceID: "crypto", FunctionName: "fn"}
	req := Request{RequestID: "r", DatasetID: "ds", Frequency: "1m", BatchKind: domain.BatchKindRealtime}
	pipeline.reportSlowItems(req, []itemTiming{{
		Recorded: true, Subject: "BTC-USDT", Fetch: 2 * time.Second, Attempts: 2,
		HTTP: httpclient.TraceSnapshot{Requests: 2, Attempts: []httpclient.TraceAttempt{
			{Domain: "api.binance.com", Phase: "wait_response", Kind: "timeout", Ms: 1200, Reused: true, IdleMs: 30000},
			{Domain: "data-api.binance.vision", Phase: "done", Status: 200, Ms: 300},
		}},
	}}, time.Time{})
	require.Len(t, reporter.entries, 1)
	require.Equal(t, "api.binance.com:wait_response:timeout:1200ms:reused30000ms|dns=0,c=0,t=0,w=0,f=0;data-api.binance.vision:done:200:300ms:new|dns=0,c=0,t=0,w=0,f=0", reporter.entries[0].Fields["attempt_trace"])
}

func TestSchedProbeAndRuntimeFields(t *testing.T) {
	probe := startSchedProbe()
	time.Sleep(60 * time.Millisecond)
	fields := probe.finish()
	lag, err := strconv.Atoi(fields["sched_lag_max_ms"])
	require.NoError(t, err)
	require.GreaterOrEqual(t, lag, 0)
	goroutines, err := strconv.Atoi(fields["goroutines_max"])
	require.NoError(t, err)
	require.Positive(t, goroutines)

	entry := time.Now()
	started := entry.Add(2400 * time.Millisecond)
	runtime := runtimeFields(Request{TimerLateMS: 900, StaggerMS: 1500, EntryAt: entry}, started)
	require.Equal(t, "900", runtime["timer_late_ms"])
	require.Equal(t, "1500", runtime["stagger_ms"])
	require.Equal(t, "2400", runtime["entry_to_start_ms"])
	require.Contains(t, runtime, "proc_age_ms")
	require.Contains(t, runtime, "invocation_seq")
	require.Contains(t, runtime, "num_cpu")
	// 非定时触发的请求没有这几项。
	require.NotContains(t, runtimeFields(Request{}, started), "timer_late_ms")
	require.NotContains(t, runtimeFields(Request{}, started), "entry_to_start_ms")
}

func TestKlinePipelinePassesStandbyDelayToProviderRequests(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 5, 0, 0, time.UTC)
	bar := marketdata.NormalizedKline{
		SubjectID: "600000.XSHG", ProviderID: "sina", SourceID: "stockcn_minute_http",
		ProviderSymbol: "sh600000", Frequency: "1m",
		BarStart: now.Add(-6 * time.Minute), BarEnd: now.Add(-5 * time.Minute),
		Open: 9, High: 9.1, Low: 8.9, Close: 9.05, VolumeShares: 1000, AmountCNY: 9050,
		ProviderTimestamp: now.Add(-5 * time.Minute), FetchedAt: now, RequestID: "standby",
	}
	var got []time.Duration
	var mu sync.Mutex
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "sina", sourceID: "stockcn_minute_http", rows: []marketdata.NormalizedKline{bar},
		ctxHook: func(ctx context.Context) {
			mu.Lock()
			got = append(got, httpclient.StandbyDelayFrom(ctx))
			mu.Unlock()
		}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{now}, nil)
	require.NoError(t, err)
	pipeline := &KlinePipeline{
		Router: router, Storage: &pipelineStorage{}, CandidateChain: []string{"sina"},
		SourceID: "stockcn_minute_http", MarketID: StockCNSpaceID, SpaceID: StockCNSpaceID,
		DatasetID: StockCNDatasetID, Now: func() time.Time { return now },
	}
	run := func(standbyMS int) {
		_, err := pipeline.Execute(context.Background(), Request{
			BatchID: "standby", BatchKind: domain.BatchKindRealtime, StandbyMS: standbyMS,
			SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
			Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", RequestID: "standby",
			Items: []domain.CollectionItem{{
				SubjectID: bar.SubjectID, Symbol: bar.ProviderSymbol, Provider: "sina", SourceID: "stockcn_minute_http",
				MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: 1,
			}},
		})
		require.NoError(t, err)
	}
	run(0)
	run(800)
	require.Equal(t, []time.Duration{0, 800 * time.Millisecond}, got)
}
