package domain

import (
	"encoding/json"
	"testing"
	"time"

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

func TestParseCollectParamsAddsExplicitDefaultKlineHistoryPolicy(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"dataset",
		"symbol_dataset_id":"binance_spot_symbols",
		"target_dataset_id":"binance_spot_kline_1m",
		"frequency":"1m"
	}`, "", "", "kline")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	require.NotNil(t, params.HistoryPolicy)
	assert.Equal(t, "live_only", params.HistoryPolicy.Mode)
	assert.Equal(t, 1000, params.HistoryPolicy.BatchBarLimit)
	assert.Equal(t, 1, params.HistoryPolicy.MaxConcurrency)
	assert.Equal(t, "0m", params.HistoryPolicy.GapRepairLookback)
	assert.InDelta(t, 1.0, params.HistoryPolicy.RateBudgetRatio, 1e-9)

	raw, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"history_policy":{"mode":"live_only"`)
}

func TestParseCollectParamsAcceptsExplicitKlineHistoryPolicy(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"symbol_source":"dataset",
		"symbol_dataset_id":"binance_spot_symbols",
		"target_dataset_id":"binance_spot_kline_1m",
		"frequency":"1m",
		"history_policy":{
			"mode":"since",
			"since":"2026-08-01T00:00:00Z",
			"batch_bar_limit":500,
			"max_concurrency":2,
			"gap_repair_lookback":"4d",
			"rate_budget_ratio":0.2
		}
	}`, "", "", "kline")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	require.NotNil(t, params.HistoryPolicy)
	assert.Equal(t, "since", params.HistoryPolicy.Mode)
	assert.Equal(t, "2026-08-01T00:00:00Z", params.HistoryPolicy.Since)
	assert.Equal(t, 500, params.HistoryPolicy.BatchBarLimit)
	assert.Equal(t, 2, params.HistoryPolicy.MaxConcurrency)
	assert.Equal(t, "4d", params.HistoryPolicy.GapRepairLookback)
	assert.InDelta(t, 0.2, params.HistoryPolicy.RateBudgetRatio, 1e-9)
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

func TestCollectParamsValidateRejectsInvalidKlineHistoryPolicy(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "lookback requires positive days",
			raw: `{
				"provider":"binance",
				"market_type":"spot",
				"symbol_source":"dataset",
				"symbol_dataset_id":"binance_spot_symbols",
				"target_dataset_id":"binance_spot_kline_1m",
				"frequency":"1m",
				"history_policy":{"mode":"lookback","lookback":0}
			}`,
			want: "lookback",
		},
		{
			name: "since requires rfc3339 time",
			raw: `{
				"provider":"binance",
				"market_type":"spot",
				"symbol_source":"dataset",
				"symbol_dataset_id":"binance_spot_symbols",
				"target_dataset_id":"binance_spot_kline_1m",
				"frequency":"1m",
				"history_policy":{"mode":"since","since":"yesterday"}
			}`,
			want: "since",
		},
		{
			name: "rate budget ratio must be bounded",
			raw: `{
				"provider":"binance",
				"market_type":"spot",
				"symbol_source":"dataset",
				"symbol_dataset_id":"binance_spot_symbols",
				"target_dataset_id":"binance_spot_kline_1m",
				"frequency":"1m",
				"history_policy":{"mode":"live_only","rate_budget_ratio":1.5}
			}`,
			want: "rate_budget_ratio",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := ParseCollectParams(tt.raw, "", "", "kline")
			require.NoError(t, err)
			require.ErrorContains(t, params.Validate(), tt.want)
		})
	}
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

func TestParseCollectParamsAcceptsKlineResampleContract(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":" MOOX ",
		"market_type":" SPOT ",
		"source_dataset_id":" binance_spot_kline_1m ",
		"source_frequency":"60m",
		"source_series_tag":" venue:binance ",
		"target_dataset_id":" spot_kline_derived_4h ",
		"target_frequency":"240m",
		"alignment":" EPOCH_UTC ",
		"settle_delay_ms":10000
	}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.NoError(t, params.Validate())

	assert.Equal(t, "moox", params.Provider)
	assert.Equal(t, "spot", params.MarketType)
	assert.Equal(t, "binance_spot_kline_1m", params.Source.DatasetID)
	assert.Equal(t, "1H", params.SourceFrequency)
	assert.Equal(t, "venue:binance", params.SourceSeriesTag)
	assert.Equal(t, "spot_kline_derived_4h", params.Target.DatasetID)
	assert.Equal(t, "4H", params.TargetFrequency)
	assert.Equal(t, "epoch_utc", params.Alignment)
	assert.Equal(t, time.Second*10, params.SettleDelay())
}

func TestParseCollectParamsRejectsResampleRepairOverride(t *testing.T) {
	_, err := ParseCollectParams(`{
		"provider":"moox",
		"market_type":"spot",
		"source_dataset_id":"binance_spot_kline_1m",
		"source_frequency":"1m",
		"source_series_tag":"venue:binance",
		"target_dataset_id":"spot_kline_derived_4h",
		"target_frequency":"4H",
		"alignment":"epoch_utc",
		"repair_lookback_buckets":3
	}`, "", "", "kline_resample")
	require.ErrorContains(t, err, "unknown field")
}

func TestKlineResampleParamsRejectInvalidPairAndIdentity(t *testing.T) {
	tests := []struct {
		name      string
		overrides string
		want      string
	}{
		{name: "same dataset", overrides: `"source_dataset_id":"same","target_dataset_id":"same"`, want: "must differ"},
		{name: "missing series", overrides: `"source_series_tag":""`, want: "source_series_tag"},
		{name: "wrong alignment", overrides: `"alignment":"session"`, want: "epoch_utc"},
		{name: "target not multiple", overrides: `"source_frequency":"1H","target_frequency":"90m"`, want: "multiple"},
		{name: "target not larger", overrides: `"source_frequency":"1H","target_frequency":"1H"`, want: "greater"},
		{name: "negative settle", overrides: `"settle_delay_ms":-1`, want: "non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc",` + tt.overrides + `}`
			params, err := ParseCollectParams(raw, "", "", "kline_resample")
			require.NoError(t, err)
			require.ErrorContains(t, params.Validate(), tt.want)
		})
	}
}

func TestKlineResampleImmutableIdentityIgnoresSettleDelay(t *testing.T) {
	left, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"60m","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"240m","alignment":"epoch_utc","settle_delay_ms":10000}`, "", "", "kline_resample")
	require.NoError(t, err)
	right, err := ParseCollectParams(`{"provider":"MOOX","market_type":"SPOT","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"EPOCH_UTC","settle_delay_ms":20000}`, "", "", "kline_resample")
	require.NoError(t, err)
	assert.NoError(t, ValidateSameResampleIdentity(left, right))

	right.SourceSeriesTag = "venue:okx"
	require.ErrorContains(t, ValidateSameResampleIdentity(left, right), "source_series_tag")
}

func TestKlineResampleCanonicalJSONOmitsRuntimeRepairPolicy(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"60m","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"240m","alignment":"epoch_utc"}`, "", "", "kline_resample")
	require.NoError(t, err)
	raw, err := params.CanonicalJSON()
	require.NoError(t, err)
	assert.Contains(t, raw, `"source_frequency":"1H"`)
	assert.Contains(t, raw, `"target_frequency":"4H"`)
	assert.NotContains(t, raw, "repair")
}

func TestKlineResampleSettleDelayPreservesExplicitZero(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"target_kline_derived_2m","target_frequency":"2m","alignment":"epoch_utc","settle_delay_ms":0}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.NotNil(t, params.SettleDelayMS)
	assert.EqualValues(t, 0, *params.SettleDelayMS)
	assert.Equal(t, time.Duration(0), params.SettleDelay())
	assert.Equal(t, time.Duration(0), params.SettleDelayOr(10*time.Second))
	raw, err := params.CanonicalJSON()
	require.NoError(t, err)
	assert.Contains(t, raw, `"settle_delay_ms":0`)
}

func TestKlineResampleRejectsDurationAndSettleDelayOverflow(t *testing.T) {
	tooLargeFrequency, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"9007199254740993m","source_series_tag":"venue:binance","target_dataset_id":"target_kline_derived_2m","target_frequency":"2m","alignment":"epoch_utc"}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.ErrorContains(t, tooLargeFrequency.Validate(), "frequency must not exceed 30 days")

	tooLargeDelay, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"target_kline_derived_2m","target_frequency":"2m","alignment":"epoch_utc","settle_delay_ms":288230376151711744}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.ErrorContains(t, tooLargeDelay.Validate(), "settle_delay_ms must not exceed")
	assert.Equal(t, MaxResampleSettleDelay, tooLargeDelay.SettleDelayOr(0))
}
