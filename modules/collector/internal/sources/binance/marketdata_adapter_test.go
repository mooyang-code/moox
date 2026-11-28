package binance

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarketDataAdapterFetchesClosedKlineThroughTypedProvider(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	start := time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		assert.Equal(t, "BTCUSDT", params.Symbol)
		assert.Equal(t, start, req.StartTime)
		assert.Equal(t, end, req.EndTime)
		return []*binanceapi.Kline{
			testBinanceKline(start, start.Add(time.Minute-time.Millisecond), 100, 110, 90, 105, 12, 1234.56),
			testBinanceKline(start.Add(time.Minute), start.Add(2*time.Minute-time.Millisecond), 105, 112, 101, 108, 8, 850),
		}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{
		InstrumentType: marketdata.InstrumentSpot,
		KlineCollector: collector,
		Now:            func() time.Time { return now },
	})
	rows, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID:       "crypto",
		ExchangeID:     "binance",
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
	assert.Equal(t, start, rows[0].BarStart)
	assert.Equal(t, start.Add(time.Minute), rows[0].BarEnd)
	assert.Equal(t, 1234.56, rows[0].Amount)
}

func TestMarketDataAdapterUsesConfiguredKlineRequestTimeout(t *testing.T) {
	t.Setenv("MOOX_FETCH_REQUEST_TIMEOUT_MS", "2000")

	defaultAdapter := NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSpot})
	require.Equal(t, 5*time.Second, defaultAdapter.KlineSpec().RateLimit.RequestTimeout, "不从进程环境读取超时，默认 5 秒")

	configured := NewMarketDataAdapter(AdapterConfig{
		InstrumentType: marketdata.InstrumentSpot,
		RateLimit:      marketdata.RateLimitPolicy{RequestTimeout: 2 * time.Second},
	})
	require.Equal(t, 2*time.Second, configured.KlineSpec().RateLimit.RequestTimeout)
}

func TestMarketDataAdapterNormalizesBinanceIntervalWithoutChangingStoredFrequency(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
		assert.Equal(t, "1h", params.Interval)
		assert.Equal(t, "1h", req.Interval)
		start := now.Add(-2 * time.Hour)
		return []*binanceapi.Kline{testBinanceKline(start, start.Add(time.Hour-time.Millisecond), 100, 110, 90, 105, 12, 1234.56)}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSpot, KlineCollector: collector, Now: func() time.Time { return now }})
	rows, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "crypto", ExchangeID: "binance", InstrumentType: marketdata.InstrumentSpot,
		SubjectID: "BTC-USDT-SPOT", ProviderSymbol: "BTCUSDT", Frequency: "1h", Limit: 1, RequestID: "req-binance-hour",
	})

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "1h", rows[0].Frequency)
}

func TestMarketDataAdapterSupportsSwapHourlyRequestThroughRouter(t *testing.T) {
	now := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
		assert.Equal(t, "SWAP", params.InstType)
		assert.Equal(t, "1h", req.Interval)
		start := now.Add(-2 * time.Hour)
		return []*binanceapi.Kline{testBinanceKline(start, start.Add(time.Hour-time.Millisecond), 100, 110, 90, 105, 12, 1234.56)}, nil
	}
	adapter := NewMarketDataAdapter(AdapterConfig{InstrumentType: marketdata.InstrumentSwap, KlineCollector: collector, Now: func() time.Time { return now }})
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(adapter))
	router, err := marketdata.NewRouter(registry, 2, nil, nil)
	require.NoError(t, err)
	rows, err := router.NewSession().FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "crypto", ExchangeID: "binance", InstrumentType: marketdata.InstrumentSwap,
		SubjectID: "1000BONK-USDT-SWAP", ProviderSymbol: "1000BONKUSDT", SourceID: "swap_http", Frequency: "1h", Limit: 1, RequestID: "req-binance-swap-hour",
	}, []string{"binance"})

	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestMarketDataAdapterFetchesTypedInstrumentSnapshot(t *testing.T) {
	snapshotAt := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewSymbolCollector()
	collector.fetchSymbolPage = func(_ context.Context, params *sources.CollectParams) ([]*binanceapi.SymbolInfo, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		return []*binanceapi.SymbolInfo{
			{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active", TradingStatus: "TRADING"},
			{Symbol: "币安人生USDT", BaseAsset: "币安人生", QuoteAsset: "USDT", Status: "active", TradingStatus: "TRADING"},
			{Symbol: "ETH-USDT", BaseAsset: "ETH", QuoteAsset: "USDT", Status: "inactive", TradingStatus: "BREAK"},
			{Symbol: "XRP-BUSD", BaseAsset: "XRP", QuoteAsset: "BUSD", Status: "active", TradingStatus: "TRADING"},
			{Symbol: "SOL-USDT", BaseAsset: "SOL", QuoteAsset: "USDT", Status: "inactive", TradingStatus: "BREAK"},
			{Symbol: "SOL-USDT", BaseAsset: "SOL", QuoteAsset: "USDT", Status: "active", TradingStatus: "TRADING"},
		}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{
		InstrumentType:  marketdata.InstrumentSpot,
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
	assert.Equal(t, snapshotAt.Format(time.RFC3339Nano), snapshot.SnapshotID)
	assert.Equal(t, map[string]int{"binance": 4}, snapshot.ExchangeCounts)
	require.Equal(t, []marketdata.Instrument{{
		SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT", Exchange: "binance", Name: "BTC/USDT",
		Status: "active", BaseAsset: "BTC", QuoteAsset: "USDT", TradingStatus: "TRADING",
	}, {
		SubjectID: "币安人生-USDT", ProviderSymbol: "币安人生USDT", Exchange: "binance", Name: "币安人生/USDT",
		Status: "active", BaseAsset: "币安人生", QuoteAsset: "USDT", TradingStatus: "TRADING",
	}, {
		SubjectID: "ETH-USDT", ProviderSymbol: "ETHUSDT", Exchange: "binance", Name: "ETH/USDT",
		Status: "inactive", BaseAsset: "ETH", QuoteAsset: "USDT", TradingStatus: "BREAK",
	}, {
		SubjectID: "SOL-USDT", ProviderSymbol: "SOLUSDT", Exchange: "binance", Name: "SOL/USDT",
		Status: "active", BaseAsset: "SOL", QuoteAsset: "USDT", TradingStatus: "TRADING",
	}}, snapshot.Instruments)
	require.NoError(t, marketdata.ValidateInstrumentSnapshot(snapshot))
}

func TestMarketDataAdapterRejectsSnapshotWhenSymbolsSkipped(t *testing.T) {
	snapshotAt := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewSymbolCollector()
	collector.fetchSymbolPage = func(_ context.Context, params *sources.CollectParams) ([]*binanceapi.SymbolInfo, error) {
		return []*binanceapi.SymbolInfo{
			{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"},
			{Symbol: "BAD USDT", BaseAsset: "BAD COIN", QuoteAsset: "USDT", Status: "active"},
		}, nil
	}

	adapter := NewMarketDataAdapter(AdapterConfig{
		InstrumentType:  marketdata.InstrumentSpot,
		SymbolCollector: collector,
		Now:             func() time.Time { return snapshotAt.Add(time.Minute) },
	})
	_, err := adapter.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "crypto",
		ExchangeID: "binance",
		SnapshotAt: snapshotAt,
		RequestID:  "req-skipped",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, marketdata.ErrProtocol)
	assert.Contains(t, err.Error(), "skipped 1 binance symbols")
}

func TestKlineCollectorUsesOnePhysicalRequestPerFetcherCall(t *testing.T) {
	collector := NewKlineCollector()
	calls := 0
	collector.fetchKlinePage = func(context.Context, *sources.CollectParams, *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
		calls++
		return []*binanceapi.Kline{testBinanceKline(time.Unix(0, 0), time.Unix(59, 0), 1, 2, 1, 2, 3, 4)}, nil
	}

	klines, err := collector.fetchKlinesOnce(context.Background(), &sources.CollectParams{
		InstType: InstTypeSPOT, Symbol: "BTCUSDT", Interval: "1m",
	}, &binanceapi.KlineRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})

	require.NoError(t, err)
	assert.Len(t, klines, 1)
	assert.Equal(t, 1, calls)
}

func TestFilterClosedKlinesExcludesOpenBar(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	closed, skipped := filterClosedKlines([]*binanceapi.Kline{
		{OpenTime: now.Add(-2 * time.Minute), CloseTime: now.Add(-time.Minute)},
		{OpenTime: now.Add(-time.Minute), CloseTime: now.Add(time.Minute)},
	}, now)

	assert.Len(t, closed, 1)
	assert.Equal(t, 1, skipped)
}

func testBinanceKline(openTime, closeTime time.Time, open, high, low, closePrice, volume, quoteVolume float64) *binanceapi.Kline {
	return &binanceapi.Kline{
		OpenTime:    openTime,
		CloseTime:   closeTime,
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closePrice,
		Volume:      volume,
		QuoteVolume: quoteVolume,
	}
}
