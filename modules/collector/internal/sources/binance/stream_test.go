package binance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "stream", name))
	require.NoError(t, err)
	return raw
}

func testStreamSource(t *testing.T, market string) stream.Source {
	t.Helper()
	source, err := NewStreamSource(market, StreamConfig{})
	require.NoError(t, err)
	return source
}

func TestStreamNamesAndLimits(t *testing.T) {
	spot, swap := testStreamSource(t, "spot"), testStreamSource(t, "swap")
	name, ok := spot.Stream("BTCUSDT", marketdata.FrequencyMinute)
	require.True(t, ok)
	require.Equal(t, "btcusdt@kline_1m", name)
	hour, _ := marketdata.ParseFrequency("1h")
	name, ok = swap.Stream("BTCUSDT", hour)
	require.True(t, ok)
	require.Equal(t, "btcusdt_perpetual@continuousKline_1h", name)
	require.Equal(t, []string{"wss://fstream.binance.com/market/stream"}, swap.Endpoints(), "K 线只在 /market 路由")
	require.Equal(t, []string{"!contractInfo"}, swap.StatusStreams())
	require.Empty(t, spot.StatusStreams())
	require.Equal(t, marketdata.SourceKey{ProviderID: "binance", SourceID: "swap_http"}, swap.Key(), "沿用 REST 的 source_id，序列身份不变")
	require.Equal(t, 1000, spot.Limits().StreamsPerConn)
	require.Equal(t, 5, swap.Limits().MessagesPerSecond)
	require.Equal(t, 24*time.Hour, swap.Limits().MaxConnAge)
	raw, err := spot.Subscribe([]string{"btcusdt@kline_1m"}, 7)
	require.NoError(t, err)
	require.JSONEq(t, `{"method":"SUBSCRIBE","params":["btcusdt@kline_1m"],"id":7}`, string(raw))
}

// 推送解码与 REST 同一根 K 线的标准化结果一致（字段含义与数值解析相同）。
func TestStreamDecodeMatchesRESTNormalization(t *testing.T) {
	received := time.Date(2025, 10, 21, 8, 0, 0, 231e6, time.UTC)
	for _, test := range []struct {
		market, file, symbol, sourceID, open, high, low, close, volume, quote string
		trades                                                                int64
	}{
		{"spot", "spot_kline_final.json", "BTCUSDT", "spot_http", "108012.34000000", "108077.00000000", "107990.10000000", "108050.01000000", "12.34567000", "1333777.12345670", 1207},
		{"swap", "swap_continuous_final.json", "BTCUSDT", "swap_http", "107980.1", "108040.0", "107950.0", "108020.5", "321.456", "34712345.6789", 1455},
	} {
		events, err := testStreamSource(t, test.market).Decode(fixture(t, test.file), received)
		require.NoError(t, err)
		require.Len(t, events, 1)
		event := events[0]
		require.Equal(t, stream.EventKlineFinal, event.Kind)
		require.Equal(t, test.symbol, event.Symbol)

		restKline := &market.Kline{OpenTime: time.UnixMilli(1761033540000), CloseTime: time.UnixMilli(1761033599999),
			Open: common.NewDecimal(test.open), High: common.NewDecimal(test.high), Low: common.NewDecimal(test.low), Close: common.NewDecimal(test.close),
			Volume: common.NewDecimal(test.volume), QuoteVolume: common.NewDecimal(test.quote), TradeCount: test.trades}
		instrument := marketdata.InstrumentSpot
		if test.market == "swap" {
			instrument = marketdata.InstrumentSwap
		}
		rest, err := normalizeMarketDataKline(marketdata.KlineRequest{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT", SourceID: test.sourceID, Frequency: "1m", InstrumentType: instrument, RequestID: "r"}, restKline, received)
		require.NoError(t, err)
		ws := event.Kline
		ws.SubjectID, ws.ProviderSymbol, ws.RequestID = rest.SubjectID, rest.ProviderSymbol, rest.RequestID
		require.Equal(t, rest, ws, test.market)
	}
}

// 大小写不同的键（t/T、v/V、q/Q、l/L、e/E）不能串值。
func TestStreamDecodeKeepsCaseSensitiveKeysApart(t *testing.T) {
	events, err := testStreamSource(t, "spot").Decode(fixture(t, "spot_kline_update.json"), time.Now())
	require.NoError(t, err)
	kline := events[0].Kline
	require.Equal(t, stream.EventKlineUpdate, events[0].Kind)
	require.Equal(t, 100.5, kline.VolumeShares, "v 不能被 V（50.25）覆盖")
	require.Equal(t, 392000.1, kline.AmountCNY, "q 不能被 Q 覆盖")
	require.Equal(t, 3899.5, kline.Low, "l 不能被 L（成交 ID）覆盖")
	require.Equal(t, time.UnixMilli(1761033540000).UTC(), kline.BarStart, "t 是开盘时间")
	require.Equal(t, time.UnixMilli(1761033599999).UTC(), kline.ProviderTimestamp, "T 是收盘时间")
	require.Equal(t, time.UnixMilli(1761033575000).UTC(), events[0].EventAt, "E 是事件时间")
}

func TestStreamDecodeControlAndStatusMessages(t *testing.T) {
	swap := testStreamSource(t, "swap")
	events, err := swap.Decode(fixture(t, "subscribe_ack.json"), time.Now())
	require.NoError(t, err)
	require.Equal(t, []stream.Event{{Kind: stream.EventControl, ID: 3}}, events)
	events, err = swap.Decode(fixture(t, "subscribe_error.json"), time.Now())
	require.NoError(t, err)
	require.Equal(t, stream.EventControl, events[0].Kind)
	require.Equal(t, 4, events[0].ID)
	require.Contains(t, events[0].Err, "Invalid request")
	events, err = swap.Decode(fixture(t, "swap_contract_info.json"), time.Now())
	require.NoError(t, err)
	require.Equal(t, stream.EventStatus, events[0].Kind)
	require.Equal(t, "ABCUSDT", events[0].Symbol)
	require.False(t, events[0].Trading)
	_, err = swap.Decode([]byte(`{"stream":"x","data":`), time.Now())
	require.Error(t, err)
	var probe map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "swap_continuous_final.json"), &probe))
}
