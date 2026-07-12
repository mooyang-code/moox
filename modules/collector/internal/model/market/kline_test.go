package market

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKline_Validate_MissingSymbol_ShouldReturnError(t *testing.T) {
	k := NewKline("binance", "", "1m")
	err := k.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "交易对不能为空")
}

func TestKline_Validate_InvalidRequiredFieldsAndRanges_ShouldReturnError(t *testing.T) {
	baseTime := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		kline   *Kline
		wantErr string
	}{
		{name: "missing exchange", kline: NewKline("", "BTCUSDT", Interval1m), wantErr: "交易所不能为空"},
		{name: "missing interval", kline: NewKline("binance", "BTCUSDT", ""), wantErr: "时间间隔不能为空"},
		{name: "missing time", kline: NewKline("binance", "BTCUSDT", Interval1m), wantErr: "时间不能为空"},
		{name: "open after close", kline: func() *Kline {
			k := NewKline("binance", "BTCUSDT", Interval1m)
			k.OpenTime = baseTime.Add(time.Minute)
			k.CloseTime = baseTime
			return k
		}(), wantErr: "开盘时间不能晚于收盘时间"},
		{name: "high below low", kline: func() *Kline {
			k := NewKline("binance", "BTCUSDT", Interval1m)
			k.OpenTime = baseTime
			k.CloseTime = baseTime.Add(time.Minute)
			k.High = common.NewDecimal("1")
			k.Low = common.NewDecimal("2")
			return k
		}(), wantErr: "最高价不能低于最低价"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.kline.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestKline_Validate_ValidKline_ShouldPass(t *testing.T) {
	open := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	closeTime := open.Add(time.Minute)
	k := NewKline("binance", "BTCUSDT", Interval1m)
	k.OpenTime = open
	k.CloseTime = closeTime
	k.Open = common.NewDecimal("100")
	k.High = common.NewDecimal("110")
	k.Low = common.NewDecimal("90")
	k.Close = common.NewDecimal("105")

	require.NoError(t, k.Validate())
}

func TestKline_MarshalUnmarshal_ShouldRoundTrip(t *testing.T) {
	k := NewKline("binance", "BTCUSDT", Interval5m)
	k.OpenTime = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	k.CloseTime = k.OpenTime.Add(5 * time.Minute)
	k.Close = common.NewDecimal("1.25")

	raw, err := k.Marshal()
	require.NoError(t, err)

	var restored Kline
	require.NoError(t, restored.Unmarshal(raw))
	assert.Equal(t, k.Symbol, restored.Symbol)
	assert.Equal(t, k.Interval, restored.Interval)
	assert.Equal(t, k.Close.String(), restored.Close.String())
}

func TestIntervalDuration_KnownInterval_ShouldReturnDuration(t *testing.T) {
	got, err := IntervalDuration(Interval1h)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, got)
}

func TestIntervalDuration_AllKnownIntervals_ShouldReturnDuration(t *testing.T) {
	tests := map[string]time.Duration{
		Interval1m:  time.Minute,
		Interval3m:  3 * time.Minute,
		Interval5m:  5 * time.Minute,
		Interval15m: 15 * time.Minute,
		Interval30m: 30 * time.Minute,
		Interval1h:  time.Hour,
		Interval2h:  2 * time.Hour,
		Interval4h:  4 * time.Hour,
		Interval6h:  6 * time.Hour,
		Interval12h: 12 * time.Hour,
		Interval1d:  24 * time.Hour,
		Interval1w:  7 * 24 * time.Hour,
		Interval1M:  30 * 24 * time.Hour,
	}

	for interval, want := range tests {
		t.Run(interval, func(t *testing.T) {
			got, err := IntervalDuration(interval)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestIntervalDuration_UnknownInterval_ShouldReturnError(t *testing.T) {
	_, err := IntervalDuration("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未知的时间间隔")
}

func TestKlineBatch_AddKline_ShouldUpdateCount(t *testing.T) {
	batch := NewKlineBatch("binance", "BTCUSDT", Interval1m)
	assert.Equal(t, 0, batch.Count)

	batch.AddKline(NewKline("binance", "BTCUSDT", Interval1m))
	assert.Equal(t, 1, batch.Count)
	assert.Len(t, batch.Klines, 1)
}
