package marketfetch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
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
