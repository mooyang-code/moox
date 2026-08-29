package binance

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMarketDataAdapterFetchesClosedMinuteKlinesThroughCollector(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	start := time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		assert.Equal(t, "BTCUSDT", params.Symbol)
		assert.Equal(t, "BTC-USDT-SPOT", params.SubjectID)
		assert.Equal(t, "1m", params.Interval)
		assert.Equal(t, start, req.StartTime)
		assert.Equal(t, end, req.EndTime)
		assert.Equal(t, 2, req.Limit)
		return []*exchange.Kline{
			testExchangeKline(start, start.Add(time.Minute-time.Millisecond), "100", "110", "90", "105", "12", "1234.56"),
			testExchangeKline(start.Add(time.Minute), start.Add(2*time.Minute-time.Millisecond), "105", "112", "101", "108", "8", "850"),
		}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{
		ProductType:    marketdata.ProductSpot,
		KlineCollector: collector,
		Now:            func() time.Time { return now },
	})
	rows, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID:       "crypto",
		ExchangeID:     "binance",
		ProductType:    marketdata.ProductSpot,
		InstrumentType: marketdata.InstrumentSpot,
		SubjectID:      "BTC-USDT-SPOT",
		ProviderSymbol: "BTCUSDT",
		Frequency:      "1m",
		Limit:          2,
		StartTime:      start,
		EndTime:        end,
		RequestID:      "req-binance",
	})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "binance", rows[0].ProviderID)
	assert.Equal(t, "BTCUSDT", rows[0].ProviderSymbol)
	assert.Equal(t, start, rows[0].BarStart)
	assert.Equal(t, start.Add(time.Minute), rows[0].BarEnd)
	assert.Equal(t, start.Add(time.Minute-time.Millisecond), rows[0].ProviderTimestamp)
	assert.Equal(t, 1234.56, rows[0].AmountCNY)
}

func TestMarketDataAdapterRoutesSwapKlinesThroughCollector(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, _ *exchange.KlineRequest) ([]*exchange.Kline, error) {
		assert.Equal(t, InstTypeSWAP, params.InstType)
		return []*exchange.Kline{
			testExchangeKline(now.Add(-time.Minute), now.Add(-time.Millisecond), "100", "110", "90", "105", "12", "1234.56"),
		}, nil
	}
	adapter := NewMarketDataAdapter(AdapterConfig{KlineCollector: collector, Now: func() time.Time { return now }})

	rows, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID:       "crypto",
		ExchangeID:     "binance",
		ProductType:    marketdata.ProductSwap,
		InstrumentType: marketdata.InstrumentSwap,
		SubjectID:      "BTC-USDT-SWAP",
		ProviderSymbol: "BTCUSDT",
		Frequency:      "1m",
		Limit:          1,
		RequestID:      "req-binance-swap",
	})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "BTC-USDT-SWAP", rows[0].SubjectID)
}

func TestMarketDataAdapterFetchesCompleteActiveUSDTInstrumentSnapshotThroughCollector(t *testing.T) {
	snapshotAt := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewSymbolCollector()
	collector.fetchSymbolPage = func(_ context.Context, params *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		return []*exchange.SymbolInfo{
			{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"},
			{Symbol: "ETH-USDT", BaseAsset: "ETH", QuoteAsset: "USDT", Status: "inactive"},
			{Symbol: "BTC-USDC", BaseAsset: "BTC", QuoteAsset: "USDC", Status: "active"},
		}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{
		ProductType:     marketdata.ProductSpot,
		SymbolCollector: collector,
		Now:             func() time.Time { return snapshotAt.Add(time.Minute) },
	})
	snapshot, err := adapter.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "crypto",
		ExchangeID: "binance",
		SnapshotAt: snapshotAt,
		RequestID:  "req-symbols",
	})

	require.NoError(t, err)
	assert.NotEmpty(t, snapshot.SnapshotID)
	_, versionErr := time.Parse(time.RFC3339Nano, snapshot.SnapshotID)
	assert.NoError(t, versionErr)
	assert.Equal(t, "binance", snapshot.SourceProvider)
	assert.Equal(t, "crypto", snapshot.MarketID)
	assert.Equal(t, snapshotAt, snapshot.FetchedAt)
	assert.True(t, snapshot.Complete)
	assert.Equal(t, 1, snapshot.PageCount)
	assert.Equal(t, map[string]int{"binance": 1}, snapshot.ExchangeCounts)
	require.Equal(t, []marketdata.Instrument{{
		SubjectID:       "BTC-USDT-SPOT",
		CanonicalSymbol: "BTC-USDT",
		ProviderSymbol:  "BTCUSDT",
		Exchange:        "binance",
		Name:            "BTC/USDT",
		Status:          "active",
		BaseAsset:       "BTC",
		QuoteAsset:      "USDT",
	}}, snapshot.Instruments)
	require.NoError(t, marketdata.ValidateInstrumentSnapshot(snapshot))
}

func testExchangeKline(openTime, closeTime time.Time, open, high, low, close, volume, quoteVolume string) *exchange.Kline {
	return &exchange.Kline{
		OpenTime:    openTime,
		CloseTime:   closeTime,
		Open:        common.NewDecimal(open),
		High:        common.NewDecimal(high),
		Low:         common.NewDecimal(low),
		Close:       common.NewDecimal(close),
		Volume:      common.NewDecimal(volume),
		QuoteVolume: common.NewDecimal(quoteVolume),
	}
}

func TestNewRuntimePipelineUsesRegisteredTypedProvider(t *testing.T) {
	pipeline, err := NewRuntimePipeline(marketdata.ProductSpot, NewKlineCollector(), NewSymbolCollector())
	require.NoError(t, err)
	require.NotNil(t, pipeline)

	assert.Equal(t, "binance", pipeline.Provider().Descriptor().ID)
	assert.Same(t, pipeline.Provider(), pipeline.KlineFetcher())
	assert.Same(t, pipeline.Provider(), pipeline.InstrumentFetcher())
}

func TestRuntimePipelinePreservesLegacyRealtimeRows(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	page := []*exchange.Kline{testExchangeKline(time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), time.Date(2026, 8, 29, 1, 30, 59, 999000000, time.UTC), "1", "2", "0.5", "1.5", "10", "15")}
	newCollector := func() *KlineCollector {
		collector := NewKlineCollector()
		collector.now = func() time.Time { return now }
		collector.fetchKlinePage = func(context.Context, *sources.CollectParams, *exchange.KlineRequest) ([]*exchange.Kline, error) {
			return page, nil
		}
		return collector
	}
	params := &sources.CollectParams{SpaceID: "crypto_market", DatasetID: "binance_spot_kline_1m", InstType: InstTypeSPOT, Symbol: "BTCUSDT", SubjectID: "BTC-USDT-SPOT", Interval: "1m"}
	legacyRows, legacyWatermark, err := newCollector().FetchRealtimeRows(context.Background(), params, 3)
	require.NoError(t, err)
	pipeline, err := NewRuntimePipeline(marketdata.ProductSpot, newCollector(), NewSymbolCollector())
	require.NoError(t, err)
	pipelineRows, pipelineWatermark, err := pipeline.FetchRealtimeRows(context.Background(), params, 3)
	require.NoError(t, err)
	require.Equal(t, legacyWatermark, pipelineWatermark)
	require.Len(t, pipelineRows, len(legacyRows))
	for index := range legacyRows {
		require.True(t, proto.Equal(legacyRows[index], pipelineRows[index]))
	}
	legacyRows, legacyWatermark, err = newCollector().FetchCatchupRows(context.Background(), params, page[0].OpenTime, 3)
	require.NoError(t, err)
	pipeline, err = NewRuntimePipeline(marketdata.ProductSpot, newCollector(), NewSymbolCollector())
	require.NoError(t, err)
	pipelineRows, pipelineWatermark, err = pipeline.FetchCatchupRows(context.Background(), params, page[0].OpenTime, 3)
	require.NoError(t, err)
	require.Equal(t, legacyWatermark, pipelineWatermark)
	require.Len(t, pipelineRows, len(legacyRows))
	for index := range legacyRows {
		require.True(t, proto.Equal(legacyRows[index], pipelineRows[index]))
	}
}

func TestRuntimePipelinePreservesLegacyInstrumentRows(t *testing.T) {
	newCollector := func() *SymbolCollector {
		collector := NewSymbolCollector()
		collector.fetchSymbolPage = func(_ context.Context, collectParams *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
			require.Equal(t, []string{"203.0.113.1"}, collectParams.DNSIPs("api.binance.com"))
			return []*exchange.SymbolInfo{{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active", MinQty: "0.001", MaxQty: "100", TickSize: "0.1", LotSize: "0.001"}}, nil
		}
		return collector
	}
	params := &sources.CollectParams{SpaceID: "crypto_market", DatasetID: "binance_spot_symbols", InstType: InstTypeSPOT, DNSRoutes: map[string]sources.DNSResolution{"api.binance.com": {IPs: []string{"203.0.113.1"}}}}
	legacyRows, legacySymbols, _, err := newCollector().FetchSymbolSnapshot(context.Background(), params)
	require.NoError(t, err)
	pipeline, err := NewRuntimePipeline(marketdata.ProductSpot, NewKlineCollector(), newCollector())
	require.NoError(t, err)
	pipelineRows, pipelineSymbols, version, err := pipeline.FetchSymbolSnapshot(context.Background(), params)
	require.NoError(t, err)
	require.Equal(t, legacySymbols, pipelineSymbols)
	require.Len(t, pipelineRows, len(legacyRows))
	for index := range legacyRows {
		legacyRows[index].GetKey().GetRecord().Version = version
		require.True(t, proto.Equal(legacyRows[index], pipelineRows[index]))
	}
}
