package binance

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarketDataAdapterFetchesClosedKlineThroughTypedProvider(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	start := time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	collector := NewKlineCollector()
	collector.fetchKlinePage = func(_ context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		assert.Equal(t, "BTCUSDT", params.Symbol)
		assert.Equal(t, start, req.StartTime)
		assert.Equal(t, end, req.EndTime)
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
	assert.Equal(t, start, rows[0].BarStart)
	assert.Equal(t, start.Add(time.Minute), rows[0].BarEnd)
	assert.Equal(t, 1234.56, rows[0].AmountCNY)
}

func TestMarketDataAdapterFetchesTypedInstrumentSnapshot(t *testing.T) {
	snapshotAt := time.Date(2026, 8, 29, 2, 0, 0, 0, time.UTC)
	collector := NewSymbolCollector()
	collector.fetchSymbolPage = func(_ context.Context, params *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
		assert.Equal(t, InstTypeSPOT, params.InstType)
		return []*exchange.SymbolInfo{
			{Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"},
			{Symbol: "ETH-USDT", BaseAsset: "ETH", QuoteAsset: "USDT", Status: "inactive"},
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
	assert.Equal(t, snapshotAt.Format(time.RFC3339Nano), snapshot.SnapshotID)
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

func TestKlineCollectorUsesOnePhysicalRequestPerFetcherCall(t *testing.T) {
	collector := NewKlineCollector()
	calls := 0
	collector.fetchKlinePage = func(context.Context, *sources.CollectParams, *exchange.KlineRequest) ([]*exchange.Kline, error) {
		calls++
		return []*exchange.Kline{testExchangeKline(time.Unix(0, 0), time.Unix(59, 0), "1", "2", "1", "2", "3", "4")}, nil
	}

	klines, err := collector.fetchKlinesOnce(context.Background(), &sources.CollectParams{
		InstType: InstTypeSPOT, Symbol: "BTCUSDT", Interval: "1m",
	}, &exchange.KlineRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})

	require.NoError(t, err)
	assert.Len(t, klines, 1)
	assert.Equal(t, 1, calls)
}

func TestFilterClosedKlinesExcludesOpenBar(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 31, 30, 0, time.UTC)
	closed, skipped := filterClosedKlines([]*market.Kline{
		{OpenTime: now.Add(-2 * time.Minute), CloseTime: now.Add(-time.Minute)},
		{OpenTime: now.Add(-time.Minute), CloseTime: now.Add(time.Minute)},
	}, now)

	assert.Len(t, closed, 1)
	assert.Equal(t, 1, skipped)
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
