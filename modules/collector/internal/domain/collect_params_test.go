package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCollectParamsKlineContract(t *testing.T) {
	params, err := ParseCollectParams(`{"frequency":"60m","output_fields":["close","open","close"],"symbols_per_function":20,"allowed_lateness_seconds":30}`, DataTypeKline)
	require.NoError(t, err)
	require.NoError(t, params.ValidateKline())
	assert.Equal(t, "1h", params.Frequency)
	assert.Equal(t, []string{"close", "open"}, params.OutputFields)
	assert.Equal(t, 20, params.SymbolsPerFunction)
	assert.Equal(t, 30, params.AllowedLatenessSeconds)
}

func TestParseCollectParamsRejectsRemovedFields(t *testing.T) {
	for _, raw := range []string{
		`{"provider":"binance"}`,
		`{"market_type":"spot"}`,
		`{"market_id":"crypto"}`,
		`{"instrument_type":"spot"}`,
		`{"source_id":"binance"}`,
		`{"series_tag":"venue:binance"}`,
		`{"subject_tags":["binance_spot"]}`,
		`{"target_dataset_id":"target-kline"}`,
		`{"alignment":"epoch_utc"}`,
		`{"exchange":"binance"}`,
		`{"market":"spot"}`,
		`{"symbol":"BTC-USDT"}`,
		`{"interval":"1m"}`,
		`{"collector":{"exchange":"binance"}}`,
		`{"target":{"job_type":"collect.binance.kline"}}`,
		`{"schedule":{"timezone":"Asia/Shanghai"}}`,
		`{"objects":["BTCUSDT"]}`,
	} {
		_, err := ParseCollectParams(raw, DataTypeKline)
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), "unknown field")
	}
}

func TestCollectParamsValidateKlineRequiresValidFrequency(t *testing.T) {
	params, err := ParseCollectParams(`{"frequency":"bad"}`, DataTypeKline)
	require.NoError(t, err)
	require.Error(t, params.ValidateKline())

	empty, err := ParseCollectParams(`{}`, DataTypeKline)
	require.NoError(t, err)
	require.ErrorContains(t, empty.ValidateKline(), "frequency")
}

func TestParseCollectParamsInvalidJSONReturnsError(t *testing.T) {
	_, err := ParseCollectParams("{", DataTypeKline)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse collect params")
}

func TestCollectParamsValidateKlineUsesWholeMinuteIntervals(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		wantErr  string
	}{
		{name: "go duration", interval: "90m"},
		{name: "day duration", interval: "2d"},
		{name: "week duration", interval: "1w"},
		{name: "month duration", interval: "1mo"},
		{name: "shorter than one minute", interval: "30s", wantErr: "whole minutes"},
		{name: "fractional minute", interval: "90s", wantErr: "whole minutes"},
		{name: "zero days", interval: "0d", wantErr: "positive"},
		{name: "fractional days", interval: "1.5d", wantErr: "positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := ParseCollectParams(`{"frequency":"`+tt.interval+`"}`, DataTypeKline)
			require.NoError(t, err)

			err = params.ValidateKline()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestParseCollectParamsAcceptsKlineResampleContract(t *testing.T) {
	params, err := ParseCollectParams(`{
		"source_task_id":" src-task ",
		"source_dataset_id":" dataset_binance_kline_1m ",
		"source_frequency":"60m",
		"source_series_tag":" venue:binance ",
		"target_frequency":"240m"
	}`, "kline_resample")
	require.NoError(t, err)

	assert.Equal(t, "src-task", params.SourceTaskID)
	assert.Equal(t, "dataset_binance_kline_1m", params.SourceDatasetID)
	assert.Equal(t, "1h", params.SourceFrequency)
	assert.Equal(t, "venue:binance", params.SourceSeriesTag)
	assert.Equal(t, "4h", params.TargetFrequency)
}

func TestParseCollectParamsRejectsResampleRemovedFields(t *testing.T) {
	for _, raw := range []string{
		`{"source_dataset_id":"s","source_frequency":"1m","target_frequency":"4h","alignment":"epoch_utc"}`,
		`{"source_dataset_id":"s","source_frequency":"1m","target_frequency":"4h","repair_lookback_buckets":3}`,
		`{"source_dataset_id":"s","source_frequency":"1m","target_frequency":"4h","settle_delay_ms":1000}`,
		`{"source_dataset_id":"s","source_frequency":"1m","target_frequency":"4h","target_dataset_id":"target"}`,
	} {
		_, err := ParseCollectParams(raw, "kline_resample")
		require.ErrorContains(t, err, "unknown field", raw)
	}
}

func TestKlineResampleCanonicalJSON(t *testing.T) {
	params, err := ParseCollectParams(`{"source_dataset_id":"source","source_frequency":"60m","source_series_tag":"venue:binance","target_frequency":"240m"}`, "kline_resample")
	require.NoError(t, err)
	raw, err := params.CanonicalJSON()
	require.NoError(t, err)
	assert.Contains(t, raw, `"source_frequency":"1h"`)
	assert.Contains(t, raw, `"target_frequency":"4h"`)
	assert.NotContains(t, raw, "target_dataset_id")
}

func TestKlineResampleAcceptsSourceTaskID(t *testing.T) {
	params, err := ParseCollectParams(`{"source_task_id":"src-task","target_frequency":"45m"}`, "kline_resample")
	require.NoError(t, err)
	assert.Equal(t, "src-task", params.SourceTaskID)
	_, err = ParseCollectParams(`{"source_task_id":"src-task","frequency":"1m"}`, DataTypeKline)
	require.Error(t, err, "source_task_id 只用于合成任务")
}
