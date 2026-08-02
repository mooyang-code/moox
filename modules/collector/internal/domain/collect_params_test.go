package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCollectParamsUsesSingleDatasetDrivenContract(t *testing.T) {
	raw := `{
		"provider":" Binance ",
		"market_type":" SPOT ",
		"symbol_source":"dataset",
		"symbol_dataset_id":"source-kline",
		"target_dataset_id":"target-kline",
		"frequency":"1h"
	}`
	params, err := ParseCollectParams(raw, "", "", "kline")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "binance", params.Collector.Exchange)
	assert.Equal(t, "spot", params.Collector.Market)
	assert.Equal(t, "kline", params.Collector.DataType)
	assert.Equal(t, "source-kline", params.Source.DatasetID)
	assert.Equal(t, "target-kline", params.Target.DatasetID)
}

func TestParseCollectParamsRequiresExchangeSymbolSnapshot(t *testing.T) {
	raw := `{
		"provider":"binance",
		"market_type":"swap",
		"symbol_source":"exchange",
		"target_dataset_id":"symbols",
		"frequency":"30m"
	}`
	params, err := ParseCollectParams(raw, "", "", "symbol")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "none", params.Source.Kind)
	assert.Equal(t, []string{"30m"}, params.Collector.Intervals)
}

func TestParseCollectParamsAcceptsExchangeSymbolSnapshot(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"exchange",
		"target_dataset_id":"symbols"
	}`, "", "", "symbol")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "none", params.Source.Kind)
	assert.Equal(t, "1h", params.Frequency)
}

func TestCollectParamsRejectsManualSymbolSnapshot(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"manual",
		"target_dataset_id":"symbols"
	}`, "", "", "symbol")
	require.NoError(t, err)
	require.ErrorContains(t, params.Validate(), "symbol_source must be exchange")
}

func TestParseCollectParamsDefaultsSymbolFrequencyToHourly(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"exchange",
		"target_dataset_id":"symbols"
	}`, "", "", "symbol")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "1h", params.Frequency)
	assert.Equal(t, "1h", params.Schedule.Interval)
}

func TestParseCollectParamsRejectsRemovedFields(t *testing.T) {
	for _, raw := range []string{
		`{"exchange":"binance"}`,
		`{"market":"spot"}`,
		`{"symbol":"BTC-USDT"}`,
		`{"interval":"1m"}`,
		`{"collector":{"exchange":"binance"}}`,
		`{"target":{"job_type":"collect.binance.kline"}}`,
		`{"schedule":{"timezone":"Asia/Shanghai"}}`,
		`{"schedule":{"intervals":["1m"]}}`,
		`{"objects":["BTCUSDT"]}`,
	} {
		_, err := ParseCollectParams(raw, "binance", "spot", "kline")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	}
}

func TestCollectParamsValidateRequiresExplicitDatasetsAndIntervals(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"dataset",
		"frequency":"bad"
	}`, "", "", "kline")
	require.NoError(t, err)
	require.Error(t, params.Validate())
}

func TestParseCollectParamsInvalidJSONReturnsError(t *testing.T) {
	_, err := ParseCollectParams("{", "binance", "spot", "kline")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse collect params")
}

func TestCollectParamsValidateUsesWholeMinuteScheduleIntervals(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		wantErr  string
	}{
		{name: "go duration", interval: "90m"},
		{name: "day duration", interval: "2d"},
		{name: "week duration", interval: "1w"},
		{name: "month duration", interval: "1M"},
		{name: "shorter than one minute", interval: "30s", wantErr: "whole minutes"},
		{name: "fractional minute", interval: "90s", wantErr: "whole minutes"},
		{name: "zero days", interval: "0d", wantErr: "positive"},
		{name: "fractional days", interval: "1.5d", wantErr: "positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := ParseCollectParams(`{
				"provider":"binance",
				"market_type":"spot",
				"symbol_source":"exchange",
				"target_dataset_id":"symbols",
				"frequency":"`+tt.interval+`"
			}`, "", "", "symbol")
			require.NoError(t, err)

			err = params.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
