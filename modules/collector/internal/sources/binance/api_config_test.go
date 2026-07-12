package binance

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	exchange "github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterSymbols_KeepsActiveUSDTPairs(t *testing.T) {
	c := &SymbolCollector{}
	filtered := c.filterSymbols([]*exchange.SymbolInfo{
		{Symbol: "BTCUSDT", QuoteAsset: "USDT", Status: "active"},
		{Symbol: "ETHBTC", QuoteAsset: "BTC", Status: "active"},
		{Symbol: "XRPUSDT", QuoteAsset: "USDT", Status: "inactive"},
	})
	require.Len(t, filtered, 1)
	assert.Equal(t, "BTCUSDT", filtered[0].Symbol)
}

func TestConvertAndFilterClosedKlines(t *testing.T) {
	now := time.Now().UTC()
	closedAt := now.Add(-2 * time.Hour)
	openAt := now.Add(-time.Hour)
	converted := convertExchangeKlines([]*exchange.Kline{{
		OpenTime: openAt, CloseTime: closedAt,
		Open: common.NewDecimal("1"), High: common.NewDecimal("2"), Low: common.NewDecimal("0.5"), Close: common.NewDecimal("1.5"),
		Volume: common.NewDecimal("10"), QuoteVolume: common.NewDecimal("15"), TradeCount: 3,
	}}, "BTCUSDT", "1h")
	require.Len(t, converted, 1)
	closed, skipped := filterClosedKlines(converted, now)
	assert.Equal(t, 0, skipped)
	assert.Len(t, closed, 1)
	assert.Equal(t, closedAt, closed[0].CloseTime)
}

func TestLatestCloseTime_FormatsRFC3339(t *testing.T) {
	ts := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	klines := []*market.Kline{{CloseTime: ts}}
	assert.Contains(t, latestCloseTime(klines), "2026-07-12")
}

func TestKlineCollector_SourceAndDataType(t *testing.T) {
	c := &KlineCollector{}
	assert.Equal(t, "binance", c.Source())
	assert.Equal(t, "kline", c.DataType())
}

func TestNormalizeFreq_ShouldNormalizeUnits(t *testing.T) {
	got, err := normalizeFreq("1h")
	require.NoError(t, err)
	assert.Equal(t, "1H", got)

	_, err = normalizeFreq("")
	assert.Error(t, err)

	got, err = normalizeFreq("5m")
	require.NoError(t, err)
	assert.Equal(t, "5m", got)
}

func TestIsKlineClosedAndFormatTime(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	kline := &market.Kline{CloseTime: past}
	assert.True(t, isKlineClosed(kline, now))
	assert.False(t, isKlineClosed(&market.Kline{CloseTime: now.Add(time.Hour)}, now))
	assert.Contains(t, formatKlineTime(past), "T")
}

func TestBuildKlineRows_ShouldEmitClosedBars(t *testing.T) {
	closedAt := time.Now().UTC().Add(-3 * time.Hour)
	openAt := closedAt.Add(-time.Hour)
	klines := []*market.Kline{{
		OpenTime: openAt, CloseTime: closedAt,
		Open: common.NewDecimal("100"), High: common.NewDecimal("110"), Low: common.NewDecimal("90"),
		Close: common.NewDecimal("105"), Volume: common.NewDecimal("12"), QuoteVolume: common.NewDecimal("1200"),
		TradeCount: 7,
	}}
	rows, err := buildKlineRows(klines, "BTCUSDT", StorageBinding{
		SpaceID: "crypto", KlineDatasetID: "kline-ds",
	}, "1H")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "crypto", rows[0].GetKey().GetSpaceId())
	assert.Equal(t, "kline-ds", rows[0].GetKey().GetDatasetId())
	assert.Equal(t, "BTCUSDT", rows[0].GetKey().GetSubjectId())
}

func TestSymbolCollector_SourceAndDataType(t *testing.T) {
	c := &SymbolCollector{}
	assert.Equal(t, "binance", c.Source())
	assert.Equal(t, "symbol", c.DataType())
}
