package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCollectParamsUsesSingleDatasetDrivenContract(t *testing.T) {
	raw := `{
		"provider":" Binance ",
		"market_type":" SPOT ",
		"subject_tags":["binance_spot"],
		"target_dataset_id":"target-kline",
		"frequency":"1h"
	}`
	params, err := ParseCollectParams(raw, "", "", "kline")
	require.NoError(t, err)
	require.NoError(t, params.Validate())
	assert.Equal(t, "binance", params.Collector.Exchange)
	assert.Equal(t, "spot", params.Collector.Market)
	assert.Equal(t, "kline", params.Collector.DataType)
	assert.Equal(t, "dataset", params.Source.Kind)
	assert.Equal(t, []string{"binance_spot"}, params.SubjectTags)
	assert.Equal(t, "target-kline", params.Target.DatasetID)
}

func TestParseCollectParamsNormalizesSelectedOutputFields(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"binance","market_type":"spot","frequency":"1m","output_fields":["close","open","close"]}`, "", "", "kline")
	require.NoError(t, err)
	assert.Equal(t, []string{"close", "open"}, params.OutputFields)
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

func TestSplitSubjectTagsRemovesAndNormalizesTransientInput(t *testing.T) {
	clean, tags, err := SplitSubjectTags(`{"provider":"binance","subject_tags":[" b ","a","a",""],"frequency":"1m"}`)
	require.NoError(t, err)
	assert.NotContains(t, clean, "subject_tags")
	assert.Equal(t, []string{"a", "b"}, tags)
}

func TestCollectParamsValidateRequiresExplicitDatasetsAndIntervals(t *testing.T) {
	params, err := ParseCollectParams(`{
		"provider":"binance",
		"market_type":"spot",
		"subject_tags":["binance_spot"],
		"target_dataset_id":"dataset_target_kline",
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
		{name: "month duration", interval: "1mo"},
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
				"subject_tags":["binance_spot"],
				"target_dataset_id":"dataset_target_kline",
				"frequency":"`+tt.interval+`"
			}`, "", "", "kline")
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
		"source_dataset_id":" dataset_binance_kline_1m ",
		"source_frequency":"60m",
		"source_series_tag":" venue:binance ",
		"target_dataset_id":" dataset_spot_kline_derived_4h ",
		"target_frequency":"240m",
		"alignment":" EPOCH_UTC ",
		"settle_delay_ms":10000
	}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.NoError(t, params.Validate())

	assert.Equal(t, "moox", params.Provider)
	assert.Equal(t, "spot", params.MarketType)
	assert.Equal(t, "dataset", params.Source.Kind)
	assert.Equal(t, "dataset_binance_kline_1m", params.Source.DatasetID)
	assert.Equal(t, "1h", params.SourceFrequency)
	assert.Equal(t, "venue:binance", params.SourceSeriesTag)
	assert.Equal(t, "dataset_spot_kline_derived_4h", params.Target.DatasetID)
	assert.Equal(t, "4h", params.TargetFrequency)
	assert.Equal(t, "epoch_utc", params.Alignment)
	assert.Equal(t, time.Second*10, params.SettleDelay())
}

func TestParseCollectParamsRejectsResampleRepairOverride(t *testing.T) {
	_, err := ParseCollectParams(`{
		"provider":"moox",
		"market_type":"spot",
		"source_dataset_id":"dataset_binance_kline_1m",
		"source_frequency":"1m",
		"source_series_tag":"venue:binance",
		"target_dataset_id":"dataset_spot_kline_derived_4h",
		"target_frequency":"4h",
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
		{name: "calendar target", overrides: `"source_frequency":"1h","target_frequency":"1w"`, want: "cannot be resampled"},
		{name: "target not larger", overrides: `"source_frequency":"1h","target_frequency":"1h"`, want: "greater"},
		{name: "negative settle", overrides: `"settle_delay_ms":-1`, want: "non-negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1h","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4h","alignment":"epoch_utc",` + tt.overrides + `}`
			params, err := ParseCollectParams(raw, "", "", "kline_resample")
			require.NoError(t, err)
			require.ErrorContains(t, params.Validate(), tt.want)
		})
	}
}

func TestKlineResampleCanonicalJSONOmitsRuntimeRepairPolicy(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"60m","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"240m","alignment":"epoch_utc"}`, "", "", "kline_resample")
	require.NoError(t, err)
	raw, err := params.CanonicalJSON()
	require.NoError(t, err)
	assert.Contains(t, raw, `"source_frequency":"1h"`)
	assert.Contains(t, raw, `"target_frequency":"4h"`)
	assert.NotContains(t, raw, "repair")
}

func TestKlineResampleSettleDelayPreservesExplicitZero(t *testing.T) {
	params, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_target_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc","settle_delay_ms":0}`, "", "", "kline_resample")
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
	tooLargeFrequency, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"9007199254740993m","source_series_tag":"venue:binance","target_dataset_id":"dataset_target_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.ErrorContains(t, tooLargeFrequency.Validate(), "unsupported frequency")

	tooLargeDelay, err := ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_target_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc","settle_delay_ms":288230376151711744}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.ErrorContains(t, tooLargeDelay.Validate(), "settle_delay_ms must not exceed")
	assert.Equal(t, MaxResampleSettleDelay, tooLargeDelay.SettleDelayOr(0))
}
