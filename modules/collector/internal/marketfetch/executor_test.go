package marketfetch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type fakeKlines struct {
	rows  []*storagepb.RowFieldUpsert
	err   error
	calls atomic.Int32
}

func (f *fakeKlines) FetchCatchupRows(context.Context, *sources.CollectParams, time.Time, int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	f.calls.Add(1)
	return f.rows, time.Now().UTC(), f.err
}

func (f *fakeKlines) FetchRealtimeRows(context.Context, *sources.CollectParams, int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	f.calls.Add(1)
	return f.rows, time.Now().UTC(), f.err
}

type fakeSymbols struct{}

func (fakeSymbols) FetchSymbolSnapshot(context.Context, *sources.CollectParams) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	return nil, nil, "", errors.New("not used")
}

type fakeSnapshotSymbols struct {
	rows []*storagepb.RowFieldUpsert
	info []*exchange.SymbolInfo
}

func (f fakeSnapshotSymbols) FetchSymbolSnapshot(context.Context, *sources.CollectParams) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	return f.rows, f.info, "v1", nil
}

type fakeStorage struct {
	rows         int
	commits      int
	registers    int
	syncPoints   int
	syncPointIDs []string
	err          error
}

type fakeReconcilingStorage struct {
	fakeStorage
	reconciles int
}

type recordingItemReporter struct{ entries []clsreporter.Entry }

func (r *recordingItemReporter) Report(entry clsreporter.Entry) { r.entries = append(r.entries, entry) }

func (f *fakeStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	f.commits++
	f.rows += len(rows)
	return f.err
}

func (f *fakeStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	f.registers++
	return f.err
}

func (f *fakeStorage) AppendDatasetSyncPoint(_ context.Context, _ string, _ string, requestID string, _ string) error {
	f.syncPoints++
	f.syncPointIDs = append(f.syncPointIDs, requestID)
	return f.err
}

func (f *fakeReconcilingStorage) ReconcileSymbolSnapshot(context.Context, string, string, []*exchange.SymbolInfo) error {
	f.reconciles++
	return f.err
}

func TestExecutorAggregatesRowsIntoOneStorageCommit(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}, {}}
	klines := &fakeKlines{rows: rows}
	storage := &fakeStorage{}
	executor := &Executor{Klines: klines, Symbols: fakeSymbols{}, Storage: storage, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	payload, err := executor.Execute(context.Background(), Request{BatchID: "b1", SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindRealtime, Provider: "binance", MarketType: "spot", Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m"}, {SubjectID: "ETH-USDT", Symbol: "ETHUSDT", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if storage.commits != 1 || storage.rows != 4 {
		t.Fatalf("storage commits=%d rows=%d, want one commit with four rows", storage.commits, storage.rows)
	}
	if payload.GetStatus() != "succeeded" || payload.GetSuccessCount() != 2 {
		t.Fatalf("payload=%s success=%d", payload.GetStatus(), payload.GetSuccessCount())
	}
	if klines.calls.Load() != 2 {
		t.Fatalf("kline calls=%d, want 2", klines.calls.Load())
	}
}

func TestExecutorWiresProductionBinanceCollectorsThroughCommonRuntimePipeline(t *testing.T) {
	executor := &Executor{
		Klines:  binance.NewKlineCollector(),
		Catchup: binance.NewKlineCollector(),
		Symbols: binance.NewSymbolCollector(),
		Storage: &fakeStorage{},
	}

	wired, err := executor.withCryptoRuntime(Request{SpaceID: "crypto_market", MarketType: "spot"})
	require.NoError(t, err)
	_, realtimeOK := wired.Klines.(*binance.RuntimePipeline)
	_, catchupOK := wired.Catchup.(*binance.RuntimePipeline)
	_, instrumentOK := wired.Symbols.(*binance.RuntimePipeline)
	assert.True(t, realtimeOK)
	assert.True(t, catchupOK)
	assert.True(t, instrumentOK)
}

func TestExecutorTurnsSuccessfulItemsIntoStorageErrors(t *testing.T) {
	klines := &fakeKlines{rows: []*storagepb.RowFieldUpsert{{}}}
	storage := &fakeStorage{err: errors.New("storage unavailable")}
	reporter := &recordingItemReporter{}
	executor := &Executor{Klines: klines, Symbols: fakeSymbols{}, Storage: storage, Reporter: reporter}
	payload, err := executor.Execute(context.Background(), Request{BatchID: "b2", SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindRealtime, Provider: "binance", MarketType: "spot", Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m"}}})
	if err != nil {
		t.Fatal(err)
	}
	if payload.GetStatus() != "failed" || payload.GetRetryCount() != 1 || payload.GetItems()[0].GetOutcome() != string(domain.ItemOutcomeStorageError) {
		t.Fatalf("payload=%+v", payload)
	}
	require.Len(t, reporter.entries, 1)
	assert.Equal(t, "0", reporter.entries[0].Fields["rows"])
}

func TestExecutorReportsDNSRouteSourceForItemFailures(t *testing.T) {
	klines := &fakeKlines{rows: []*storagepb.RowFieldUpsert{{}}}
	reporter := &recordingItemReporter{}
	executor := &Executor{Klines: klines, Symbols: fakeSymbols{}, Storage: &fakeStorage{err: errors.New("storage unavailable")}, Reporter: reporter}
	_, err := executor.Execute(context.Background(), Request{
		BatchID: "dns-batch", SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindRealtime,
		Provider: "binance", MarketType: "swap", DNSRoutes: map[string]sources.DNSResolution{
			"FAPI.BINANCE.COM.": {IPs: []string{"203.0.113.7"}, ResolvedAt: time.Now().UTC().Add(-time.Minute)},
		},
		Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: "swap", DataType: "kline", DatasetID: "bars", Frequency: "1m"}},
	})
	require.NoError(t, err)
	require.Len(t, reporter.entries, 1)
	assert.Equal(t, "scf_snapshot", reporter.entries[0].Fields["dns_source"])
	assert.Equal(t, "1", reporter.entries[0].Fields["dns_route_count"])
}

func TestResultErrorSummaryUsesFirstActionableItemError(t *testing.T) {
	results := []domain.ItemResult{
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: ""},
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: "write time-series rows: timeout"},
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: "later error"},
	}
	assert.Equal(t, "write time-series rows: timeout", resultErrorSummary(results))
	assert.Empty(t, resultErrorSummary([]domain.ItemResult{{Outcome: domain.ItemOutcomeSuccess}}))
}

func TestClassifyErrorRecognizesTypedMarketDataFailures(t *testing.T) {
	assert.Equal(t, domain.ItemOutcomeHTTP429, classifyError(marketdata.ErrRateLimited))
	assert.Equal(t, domain.ItemOutcomeNetworkError, classifyError(marketdata.ErrTimeout))
}

func TestStorageAndPublishReservesKeepsFullStorageTimeout(t *testing.T) {
	commit, publish := storageAndPublishReserves(5*time.Second, 800*time.Millisecond, true)
	assert.Equal(t, 8800*time.Millisecond, commit)
	assert.Equal(t, 3*time.Second, publish)

	commit, publish = storageAndPublishReserves(0, 0, true)
	assert.Equal(t, 8*time.Second, commit)
	assert.Equal(t, 3*time.Second, publish)

	commit, publish = storageAndPublishReserves(5*time.Second, 800*time.Millisecond, false)
	assert.Equal(t, 5800*time.Millisecond, commit)
	assert.Zero(t, publish)
}

func TestExecutorSupportsSymbolSnapshotAndCatchupBatches(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}}
	storage := &fakeStorage{}
	klines := &fakeKlines{rows: rows}
	executor := &Executor{Klines: klines, Catchup: klines, Symbols: fakeSnapshotSymbols{rows: rows, info: []*exchange.SymbolInfo{{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"}}}, Storage: storage}
	symbolPayload, err := executor.Execute(context.Background(), Request{BatchID: "symbol", SpaceID: "crypto", BatchKind: domain.BatchKindSymbolSnapshot, Provider: "binance", MarketType: "spot", DatasetID: "symbols", Items: []domain.CollectionItem{{DataType: "symbol", DatasetID: "symbols"}}})
	if err != nil {
		t.Fatal(err)
	}
	if symbolPayload.GetStatus() != "succeeded" || storage.commits != 1 {
		t.Fatalf("symbol payload=%s commits=%d", symbolPayload.GetStatus(), storage.commits)
	}
	start := time.Now().UTC().Add(-time.Hour)
	catchupPayload, err := executor.Execute(context.Background(), Request{BatchID: "catchup", SpaceID: "crypto", BatchKind: domain.BatchKindCatchup, Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Items: []domain.CollectionItem{{DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", StartTime: start.Format(time.RFC3339Nano), BarLimit: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	if catchupPayload.GetStatus() != "succeeded" || klines.calls.Load() != 1 || storage.syncPoints != 1 {
		t.Fatalf("catchup payload=%s calls=%d sync_points=%d", catchupPayload.GetStatus(), klines.calls.Load(), storage.syncPoints)
	}
	assert.Equal(t, []string{"catchup"}, storage.syncPointIDs)
}

func TestExecutorKeepsLogicalCatchupSyncPointAcrossRetryBatch(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	storage := &fakeStorage{}
	klines := &fakeKlines{rows: []*storagepb.RowFieldUpsert{{}}}
	executor := &Executor{Klines: klines, Catchup: klines, Symbols: fakeSymbols{}, Storage: storage}
	_, err := executor.Execute(context.Background(), Request{BatchID: "retry-b1", SyncPointID: "initial-b0", SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindCatchup, Provider: "binance", MarketType: "spot", Frequency: "1m", Items: []domain.CollectionItem{{DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", StartTime: start.Format(time.RFC3339Nano), BarLimit: 1000}}})
	require.NoError(t, err)
	require.Equal(t, []string{"initial-b0"}, storage.syncPointIDs)
}

func TestExecutorDoesNotAppendCatchupSyncPointForEmptyOrFailedFetch(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	for name, klines := range map[string]*fakeKlines{
		"empty":  {rows: nil},
		"failed": {err: errors.New("upstream timeout")},
	} {
		t.Run(name, func(t *testing.T) {
			storage := &fakeStorage{}
			executor := &Executor{Klines: klines, Catchup: klines, Symbols: fakeSymbols{}, Storage: storage}
			payload, err := executor.Execute(context.Background(), Request{
				BatchID: "catchup-" + name, SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindCatchup,
				Provider: "binance", MarketType: "spot", Frequency: "1m",
				Items: []domain.CollectionItem{{DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", StartTime: start, BarLimit: 1000}},
			})
			require.NoError(t, err)
			require.NotEqual(t, "succeeded", payload.GetStatus())
			assert.Zero(t, storage.commits)
			assert.Zero(t, storage.syncPoints, "failed catchup must not create a View fence")
		})
	}
}

func TestExecutorDoesNotReconcilePartialSymbolSnapshotShard(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}}
	storage := &fakeReconcilingStorage{}
	executor := &Executor{
		Klines:  &fakeKlines{},
		Symbols: fakeSnapshotSymbols{rows: rows, info: []*exchange.SymbolInfo{{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"}}},
		Storage: storage,
	}
	payload, err := executor.Execute(context.Background(), Request{
		BatchID: "symbol-shard", SpaceID: "crypto", BatchKind: domain.BatchKindSymbolSnapshot,
		Provider: "binance", MarketType: "spot", DatasetID: "symbols",
		Items: []domain.CollectionItem{{DataType: "symbol", DatasetID: "symbols", SnapshotShardIndex: 1, SnapshotShardCount: 32}},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", payload.GetStatus())
	assert.Zero(t, storage.reconciles)
}

func TestExecutorReconcilesFullCatalogFromFirstSymbolSnapshotShard(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}}
	storage := &fakeReconcilingStorage{}
	executor := &Executor{
		Klines:  &fakeKlines{},
		Symbols: fakeSnapshotSymbols{rows: rows, info: []*exchange.SymbolInfo{{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"}}},
		Storage: storage,
	}
	payload, err := executor.Execute(context.Background(), Request{
		BatchID: "symbol-first-shard", SpaceID: "crypto", BatchKind: domain.BatchKindSymbolSnapshot,
		Provider: "binance", MarketType: "spot", DatasetID: "symbols",
		Items: []domain.CollectionItem{{DataType: "symbol", DatasetID: "symbols", SnapshotShardIndex: 0, SnapshotShardCount: 32}},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", payload.GetStatus())
	assert.Equal(t, 1, storage.reconciles)
}

func TestRequestRejectsOversizedRealtimeAndAcceptsSymbolSnapshotWithoutSubject(t *testing.T) {
	items := make([]domain.CollectionItem, MaxRealtimeItems+1)
	if err := (&Request{BatchID: "too-many", SpaceID: "crypto", DatasetID: "bars", BatchKind: domain.BatchKindRealtime, Items: items}).validate(); err == nil {
		t.Fatal("expected oversized realtime request to be rejected")
	}
	if err := (&Request{BatchID: "symbol", SpaceID: "crypto", DatasetID: "symbols", BatchKind: domain.BatchKindSymbolSnapshot, Items: []domain.CollectionItem{{DataType: "symbol", DatasetID: "symbols"}}}).validate(); err != nil {
		t.Fatalf("symbol snapshot without subject should be accepted: %v", err)
	}
}

func TestSymbolSnapshotTimeoutUsesDedicatedFullSnapshotSetting(t *testing.T) {
	t.Setenv("MOOX_FETCH_REQUEST_TIMEOUT_MS", "2000")
	t.Setenv("MOOX_FETCH_SYMBOL_SNAPSHOT_TIMEOUT_MS", "")
	assert.Equal(t, 2*time.Second, requestTimeout("MOOX_FETCH_REQUEST_TIMEOUT_MS", 2000))
	assert.Equal(t, 5*time.Second, requestTimeout("MOOX_FETCH_SYMBOL_SNAPSHOT_TIMEOUT_MS", 5000))

	t.Setenv("MOOX_FETCH_SYMBOL_SNAPSHOT_TIMEOUT_MS", "4500")
	assert.Equal(t, 4500*time.Millisecond, requestTimeout("MOOX_FETCH_SYMBOL_SNAPSHOT_TIMEOUT_MS", 5000))
}

func TestSelectSymbolSnapshotShardKeepsRowsAndSymbolsAligned(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}, {}, {}, {}, {}}
	symbols := []*exchange.SymbolInfo{{Symbol: "A"}, {Symbol: "B"}, {Symbol: "C"}, {Symbol: "D"}, {Symbol: "E"}}
	shardRows, shardSymbols, err := selectSymbolSnapshotShard(rows, symbols, 1, 3)
	require.NoError(t, err)
	assert.Len(t, shardRows, 2)
	assert.Equal(t, []string{"B", "C"}, []string{shardSymbols[0].Symbol, shardSymbols[1].Symbol})
}

func TestHandlerRejectsRequestForAnotherSpaceBeforeStorage(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	handler := NewHandler()
	handler.NewStorage = func(string, string, string) (Storage, error) {
		t.Fatal("storage must not be created for a cross-space request")
		return nil, nil
	}
	rsp, err := handler.Handle(context.Background(), model.CloudFunctionEvent{
		RequestID: "request-1",
		Data: map[string]any{
			"batch_id": "batch-1", "space_id": "research", "dataset_id": "bars", "provider": "binance", "market_type": "spot",
			"items": []any{map[string]any{"task_id": "task-1", "subject_id": "BTC-USDT", "symbol": "BTCUSDT", "dataset_id": "bars", "provider": "binance", "market_type": "spot", "data_type": "kline", "frequency": "1m", "bar_limit": 1}},
		},
	})
	require.NoError(t, err)
	assert.False(t, rsp.Success)
	assert.Contains(t, rsp.Message, "does not match function space")
}

func TestHandlerUsesRuntimeFunctionNameForStorageWriteSource(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	var writeSource string
	handler := NewHandler()
	handler.NewStorage = func(_, _, source string) (Storage, error) {
		writeSource = source
		return &fakeStorage{}, nil
	}
	handler.Publish = func(context.Context, Request, proto.Message) error { return nil }
	handler.Execute = func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
		return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
	}
	_, err := handler.HandleWithFunctionName(context.Background(), model.CloudFunctionEvent{
		RequestID: "request-1", StorageRPCGatewayTarget: "ip://127.0.0.1:11003",
		Data: map[string]any{
			"batch_id": "batch-1", "space_id": "crypto", "dataset_id": "bars", "provider": "binance", "market_type": "spot",
			"items": []any{map[string]any{"task_id": "task-1", "subject_id": "BTC-USDT", "symbol": "BTCUSDT", "dataset_id": "bars", "provider": "binance", "market_type": "spot", "data_type": "kline", "frequency": "1m", "bar_limit": 1}},
		},
	}, "actual-function")
	require.NoError(t, err)
	assert.Equal(t, "scf:actual-function", writeSource)
}

func TestHandlerUsesEnvironmentDNSRoutesForInvokeWhenPayloadOmitsThem(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", `{"FAPI.BINANCE.COM.":["203.0.113.9"]}`)
	var captured Request
	handler := NewHandler()
	handler.NewStorage = func(string, string, string) (Storage, error) { return &fakeStorage{}, nil }
	handler.Publish = func(context.Context, Request, proto.Message) error { return nil }
	handler.Execute = func(_ context.Context, request Request, _ Storage) (*marketfetchpb.MarketFetchBatchCompleted, error) {
		captured = request
		return &marketfetchpb.MarketFetchBatchCompleted{Status: "succeeded"}, nil
	}
	_, err := handler.HandleWithFunctionName(context.Background(), model.CloudFunctionEvent{
		RequestID: "request-1", StorageRPCGatewayTarget: "ip://127.0.0.1:11003",
		Data: map[string]any{
			"batch_id": "batch-1", "space_id": "crypto", "dataset_id": "symbols", "batch_kind": "symbol_snapshot", "provider": "binance", "market_type": "swap",
			"items": []any{map[string]any{"dataset_id": "symbols", "data_type": "symbol"}},
		},
	}, "moox-fetcher-crypto-market-invoke-ap-beijing-0")
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.9"}, captured.DNSRoutes["fapi.binance.com"].IPs)
}

func TestMergeDNSRoutesPrefersEnvironmentAddresses(t *testing.T) {
	merged := mergeDNSRoutes(
		map[string]sources.DNSResolution{"fapi.binance.com": {IPs: []string{"203.0.113.9"}}},
		map[string]sources.DNSResolution{"FAPI.BINANCE.COM.": {IPs: []string{"203.0.113.10", "203.0.113.9"}}},
	)
	require.Equal(t, []string{"203.0.113.9", "203.0.113.10"}, merged["fapi.binance.com"].IPs)
}

func TestMergeDNSRoutesRetainsPayloadAfterEnvironmentCap(t *testing.T) {
	envIPs := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4"}
	merged := mergeDNSRoutes(
		map[string]sources.DNSResolution{"fapi.binance.com": {IPs: envIPs}},
		map[string]sources.DNSResolution{"fapi.binance.com": {IPs: []string{"203.0.113.5"}}},
	)
	assert.Equal(t, append(envIPs, "203.0.113.5"), merged["fapi.binance.com"].IPs)
}
