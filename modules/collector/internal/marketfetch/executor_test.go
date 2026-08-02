package marketfetch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func (fakeSymbols) FetchSymbolSnapshot(context.Context, *sources.CollectParams, []string) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	return nil, nil, "", errors.New("not used")
}

type fakeSnapshotSymbols struct {
	rows []*storagepb.RowFieldUpsert
	info []*exchange.SymbolInfo
}

func (f fakeSnapshotSymbols) FetchSymbolSnapshot(context.Context, *sources.CollectParams, []string) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	return f.rows, f.info, "v1", nil
}

type fakeStorage struct {
	rows      int
	commits   int
	registers int
	err       error
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

func TestResultErrorSummaryUsesFirstActionableItemError(t *testing.T) {
	results := []domain.ItemResult{
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: ""},
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: "write time-series rows: timeout"},
		{Outcome: domain.ItemOutcomeStorageError, ErrorSummary: "later error"},
	}
	assert.Equal(t, "write time-series rows: timeout", resultErrorSummary(results))
	assert.Empty(t, resultErrorSummary([]domain.ItemResult{{Outcome: domain.ItemOutcomeSuccess}}))
}

func TestStorageAndPublishReservesKeepsFullStorageTimeout(t *testing.T) {
	commit, publish := storageAndPublishReserves(5*time.Second, 800*time.Millisecond)
	assert.Equal(t, 8800*time.Millisecond, commit)
	assert.Equal(t, 3*time.Second, publish)

	commit, publish = storageAndPublishReserves(0, 0)
	assert.Equal(t, 8*time.Second, commit)
	assert.Equal(t, 3*time.Second, publish)
}

func TestExecutorSupportsSymbolSnapshotAndCatchupBatches(t *testing.T) {
	rows := []*storagepb.RowFieldUpsert{{}}
	storage := &fakeStorage{}
	klines := &fakeKlines{rows: rows}
	executor := &Executor{Klines: klines, Catchup: klines, Symbols: fakeSnapshotSymbols{rows: rows, info: []*exchange.SymbolInfo{{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"}}}, Storage: storage}
	symbolPayload, err := executor.Execute(context.Background(), Request{BatchID: "symbol", SpaceID: "crypto", BatchKind: domain.BatchKindSymbolSnapshot, Provider: "binance", MarketType: "spot", DatasetID: "symbols", Items: []domain.CollectionItem{{DataType: "symbol", DatasetID: "symbols", Allowlist: []string{"BTC-USDT"}}}})
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
	if catchupPayload.GetStatus() != "succeeded" || klines.calls.Load() != 1 {
		t.Fatalf("catchup payload=%s calls=%d", catchupPayload.GetStatus(), klines.calls.Load())
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
	handler.NewStorage = func(string, string) (Storage, error) {
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
