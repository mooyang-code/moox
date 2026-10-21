package binance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

// testdata/stream/recorded 是 2026-10-21 在海外机（香港）录制的真实报文：同时段 200 根收盘 K 线与 REST 逐字段一致。
// 这里核对解码结果与报文里的原值一一对应（大小写键：v/V、q/Q 不混淆），事件种类与订阅回执正确。
func TestDecodeRecordedBinanceMessages(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "stream", "recorded", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		name := filepath.Base(file)
		market := strings.SplitN(name, "_", 2)[0]
		source, err := NewStreamSource(market, StreamConfig{})
		require.NoError(t, err)
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		events, err := source.Decode(raw, time.Now())
		require.NoError(t, err, name)
		require.Len(t, events, 1, name)
		event := events[0]
		switch {
		case strings.Contains(name, "_ack_"):
			require.Equal(t, stream.EventControl, event.Kind, name)
			require.Empty(t, event.Err, name)
		case strings.Contains(name, "_kline_update_"):
			require.Equal(t, stream.EventKlineUpdate, event.Kind, name)
		case strings.Contains(name, "_kline_final_"):
			require.Equal(t, stream.EventKlineFinal, event.Kind, name)
			var envelope struct {
				Data struct {
					Symbol string `json:"s"`
					Pair   string `json:"ps"`
					K      map[string]any
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(raw, &envelope))
			k := envelope.Data.K
			num := func(key string) float64 {
				v, err := strconv.ParseFloat(k[key].(string), 64)
				require.NoError(t, err)
				return v
			}
			require.Equal(t, strings.ToUpper(envelope.Data.Symbol+envelope.Data.Pair), event.Symbol, name)
			require.Equal(t, int64(k["t"].(float64)), event.Kline.BarStart.UnixMilli(), name)
			require.Equal(t, time.Minute, event.Kline.BarEnd.Sub(event.Kline.BarStart), name)
			require.Equal(t, []float64{num("o"), num("h"), num("l"), num("c"), num("v"), num("q")},
				[]float64{event.Kline.Open, event.Kline.High, event.Kline.Low, event.Kline.Close, event.Kline.VolumeShares, event.Kline.AmountCNY}, name)
			require.Equal(t, int64(k["n"].(float64)), event.Kline.TradeCount, name)
		default:
			t.Fatalf("unexpected fixture %s", name)
		}
	}
}
