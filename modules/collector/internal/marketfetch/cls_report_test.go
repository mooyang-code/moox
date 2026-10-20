package marketfetch

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/stretchr/testify/require"
)

type recordingReporter struct {
	mu      sync.Mutex
	entries []clsreporter.Entry
	flushed int
	err     error
}

func (r *recordingReporter) Report(entry clsreporter.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
}

func (r *recordingReporter) Flush(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushed++
	return r.err
}

func TestReportItemFailureAndBatchCarryTheFieldsNeededForAnalysis(t *testing.T) {
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{Reporter: reporter, SpaceID: "crypto", FunctionName: "moox-fetch-plan-crypto-swap-hongkong-000"}
	req := Request{RequestID: "req-1", DatasetID: "ds", Frequency: "1m", BatchKind: domain.BatchKindRealtime}
	item := domain.CollectionItem{SubjectID: "BTC-USDT", Symbol: "BTCUSDT"}
	cause := errors.New("timeout: context deadline exceeded")
	result := failureResult(item, domain.ItemOutcomeNetworkError, "network_error", cause)
	start := time.Date(2026, 10, 12, 3, 14, 0, 0, time.UTC)
	pipeline.reportItemFailure(req, result, "binance", 3, 4200*time.Millisecond, start, start.Add(time.Minute), cause)
	pipeline.reportBatch(req, 48, 1, 5*time.Second, 40*time.Second)

	require.Len(t, reporter.entries, 2)
	failure := reporter.entries[0].Fields
	require.Equal(t, "market_fetch_item_failure", failure["log_type"])
	require.Equal(t, "BTC-USDT", failure["subject_id"])
	require.Equal(t, "3", failure["attempts"])
	require.Equal(t, "4200", failure["fetch_ms"])
	require.Equal(t, "network_error", failure["error_type"])
	require.Contains(t, failure["error"], "context deadline exceeded")
	require.Equal(t, "2026-10-12T03:14:00Z", failure["window_start"])
	batch := reporter.entries[1].Fields
	require.Equal(t, "market_fetch_batch", batch["log_type"])
	require.Equal(t, "48", batch["planned"])
	require.Equal(t, "1", batch["failed"])
}

func TestFlushReportsIsBoundedAndNeverFails(t *testing.T) {
	reporter := &recordingReporter{err: errors.New("cls unavailable")}
	handler := &Handler{Reporter: reporter}
	handler.flushReports(context.Background(), time.Second)
	require.Equal(t, 1, reporter.flushed)
	// 没有配置上报器、或预算用尽时直接返回。
	(&Handler{}).flushReports(context.Background(), time.Second)
	handler.flushReports(context.Background(), 0)
	require.Equal(t, 1, reporter.flushed)
}

// 慢上报不能吃掉函数返回需要的时间：CLS 与指标共享预算，整体耗时不超过预算。
type blockingReporter struct{ recordingReporter }

func (r *blockingReporter) Flush(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type slowMetrics struct{ took time.Duration }

func (m *slowMetrics) Handle(ctx context.Context) error {
	start := time.Now()
	<-ctx.Done()
	m.took = time.Since(start)
	return ctx.Err()
}

func TestReportMetricsSharesOneBudgetBetweenCLSAndMetrics(t *testing.T) {
	metrics := &slowMetrics{}
	handler := &Handler{Reporter: &blockingReporter{}, MetricsReporter: metrics, CLSReserve: 800 * time.Millisecond}
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	handler.reportMetrics(parent)
	elapsed := time.Since(started)
	require.Less(t, elapsed, 1100*time.Millisecond, "两类上报总共不超过预算（800ms）加调度余量")
	require.Greater(t, metrics.took, 150*time.Millisecond, "指标仍分到了预算的剩余部分")

	// 距函数截止时间不足一个预算时，上报必须在“截止时间减返回余量”之前结束，给函数返回留出时间。
	short, cancelShort := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShort()
	deadline, _ := short.Deadline()
	started = time.Now()
	handler.reportMetrics(short)
	require.Less(t, time.Until(deadline), 500*time.Millisecond)
	require.GreaterOrEqual(t, time.Until(deadline), reportReturnMargin-40*time.Millisecond, "上报结束时离函数截止时间至少还有返回余量")

	// 剩余时间已不足返回余量：直接跳过，不调用任何上报器。
	reporter := &recordingReporter{}
	skipped := &Handler{Reporter: reporter}
	tight, cancelTight := context.WithTimeout(context.Background(), reportReturnMargin/2)
	defer cancelTight()
	skipped.reportMetrics(tight)
	require.Zero(t, reporter.flushed)
}

// 上报的每个字段都必须在 CLS 主题的键值索引里，否则无法按字段检索；索引字段清单在 cloudprovider 里统一维护。
func TestReportedFieldsAreAllInTheCLSIndex(t *testing.T) {
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{Reporter: reporter, SpaceID: "crypto", FunctionName: "fn"}
	req := Request{RequestID: "r", DatasetID: "ds", Frequency: "1m", BatchKind: domain.BatchKindRealtime}
	cause := errors.New("boom")
	pipeline.reportItemFailure(req, failureResult(domain.CollectionItem{SubjectID: "S", Symbol: "SYM"}, domain.ItemOutcomeNetworkError, "network_error", cause), "binance", 3, time.Second, time.Now(), time.Now(), cause)
	closeAt := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	timings := []itemTiming{{
		Recorded: true, Subject: "S", Symbol: "SYM", Provider: "binance", Outcome: domain.ItemOutcomeSuccess,
		Queue: 10 * time.Millisecond, Fetch: 1500 * time.Millisecond, StartedAt: closeAt.Add(time.Second), Attempts: 2,
		HTTP: httpclient.TraceSnapshot{Requests: 2, NewConns: 2, DNSMs: 1, ConnectMs: 30, TLSMs: 60, TTFBMs: 800, LastStatus: 200, LastDomain: "api.binance.com", RemoteIP: "1.2.3.4:443",
			Attempts: []httpclient.TraceAttempt{{Domain: "api.binance.com", Phase: "wait_response", Kind: "timeout", Ms: 1200, Reused: true}, {Domain: "api.binance.com", Phase: "done", Status: 200, Ms: 300}}},
	}}
	pipeline.reportBatch(req, 1, 1, time.Second, time.Second, batchTimingFields(timings, closeAt.Add(time.Second), closeAt.Add(3*time.Second), closeAt))
	probe := startSchedProbe()
	probeFields := probe.finish()
	pipeline.reportBatch(req, 1, 0, time.Second, time.Second, probeFields, runtimeFields(Request{TimerLateMS: 900, StaggerMS: 1500, EntryAt: closeAt}, closeAt.Add(2*time.Second)))
	pipeline.reportSlowItems(req, timings, closeAt)
	pipeline.reportCommit(req, 25, 1, 120*time.Millisecond, closeAt.Add(4*time.Second), closeAt, errors.New("storage busy"))
	indexed := map[string]bool{}
	for _, field := range tencent.MarketFetchLogIndexFields {
		indexed[field.Key] = true
	}
	for _, entry := range reporter.entries {
		for key := range entry.Fields {
			require.True(t, indexed[key], "上报字段 %s 没有建键值索引", key)
		}
	}
	// 索引里的数值字段必须能被解析为整数。
	numeric := map[string]bool{}
	for _, field := range tencent.MarketFetchLogIndexFields {
		numeric[field.Key] = field.Numeric
	}
	for _, entry := range reporter.entries {
		for key, value := range entry.Fields {
			if numeric[key] {
				_, err := strconv.ParseInt(value, 10, 64)
				require.NoError(t, err, key)
			}
		}
	}
}
