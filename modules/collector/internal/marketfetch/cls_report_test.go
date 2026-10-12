package marketfetch

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
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
	handler.flushReports(context.Background())
	require.Equal(t, 1, reporter.flushed)
	// 没有配置上报器、或调用窗口已用尽时直接返回。
	(&Handler{}).flushReports(context.Background())
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	handler.flushReports(expired)
	require.Equal(t, 1, reporter.flushed)
}

// 上报的每个字段都必须在 CLS 主题的键值索引里，否则无法按字段检索；索引字段清单在 cloudprovider 里统一维护。
func TestReportedFieldsAreAllInTheCLSIndex(t *testing.T) {
	reporter := &recordingReporter{}
	pipeline := &KlinePipeline{Reporter: reporter, SpaceID: "crypto", FunctionName: "fn"}
	req := Request{RequestID: "r", DatasetID: "ds", Frequency: "1m", BatchKind: domain.BatchKindRealtime}
	cause := errors.New("boom")
	pipeline.reportItemFailure(req, failureResult(domain.CollectionItem{SubjectID: "S", Symbol: "SYM"}, domain.ItemOutcomeNetworkError, "network_error", cause), "binance", 3, time.Second, time.Now(), time.Now(), cause)
	pipeline.reportBatch(req, 1, 1, time.Second, time.Second)
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
