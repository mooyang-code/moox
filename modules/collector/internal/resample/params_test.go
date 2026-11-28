package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/stretchr/testify/require"
)

func parseResampleParams(t *testing.T, source, target string, overrides string) *domain.CollectParams {
	t.Helper()
	raw := `{"source_dataset_id":"dataset_source","source_frequency":"` + source + `","source_series_tag":"venue:binance","target_dataset_id":"dataset_target","target_frequency":"` + target + `"` + overrides + `}`
	params, err := domain.ParseCollectParams(raw, "", "", "kline_resample")
	require.NoError(t, err)
	return params
}

func TestValidateTaskParamsAcceptsTaskOwnedTargetWithoutFrequencySuffix(t *testing.T) {
	// 结果 Dataset ID 由任务派生，不带频率后缀；这是启动时清单刷新曾因后缀检查失败的场景。
	require.NoError(t, ValidateTaskParamsForSource(sourceInfoForSpace("crypto"), parseResampleParams(t, "1h", "4h", "")))
}

// 归约规则表之外的字段（如资金费率）只有输出字段显式要这一列时才拒绝，源数据集带这一列不影响。
func TestValidateTaskParamsRejectsNonReducibleField(t *testing.T) {
	require.NoError(t, ValidateTaskParamsForSource(sourceInfoForSpace("crypto"), parseResampleParams(t, "1h", "4h", `,"output_fields":["close"]`)))
	err := ValidateTaskParamsForSource(sourceInfoForSpace("crypto"), parseResampleParams(t, "1h", "4h", `,"output_fields":["close","funding_rate"]`))
	require.ErrorContains(t, err, "funding_rate")
}

func TestValidateTaskParamsRejectsInvalidPairAndIdentity(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		target    string
		overrides string
		want      string
	}{
		{name: "same dataset", source: "1h", target: "4h", overrides: `,"source_dataset_id":"dataset_same","target_dataset_id":"dataset_same"`, want: "must differ"},
		{name: "missing series", source: "1h", target: "4h", overrides: `,"source_series_tag":""`, want: "source_series_tag"},
		{name: "calendar target", source: "1h", target: "1w", want: "cannot be resampled"},
		{name: "target not larger", source: "1h", target: "1h", want: "greater"},
		{name: "bad dataset id", source: "1h", target: "4h", overrides: `,"target_dataset_id":"target"`, want: "dataset_"},
		{name: "not a multiple", source: "30m", target: "45m", want: "whole multiple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, ValidateTaskParamsForSource(sourceInfoForSpace("crypto"), parseResampleParams(t, tt.source, tt.target, tt.overrides)), tt.want)
		})
	}
}

func TestValidateTaskParamsEpochAcceptsNonStandardDivisorsOfOneDay(t *testing.T) {
	src := sourceInfoForSpace("crypto")
	for _, pair := range [][2]string{{"5m", "45m"}, {"15m", "45m"}, {"1m", "45m"}, {"5m", "2h"}, {"1h", "12h"}, {"15m", "1d"}} {
		require.NoError(t, ValidateTaskParamsForSource(src, parseResampleParams(t, pair[0], pair[1], "")), "%s→%s", pair[0], pair[1])
	}
}

func TestValidateTaskParamsSessionDatasetRejectsPeriodsThatDoNotDivideSessions(t *testing.T) {
	src := sourceInfoForSpace("stockcn")
	err := ValidateTaskParamsForSource(src, parseResampleParams(t, "1m", "45m", ""))
	require.ErrorContains(t, err, "09:30–11:30")
	require.ErrorContains(t, err, "可选的日内周期")
	require.ErrorContains(t, ValidateTaskParamsForSource(src, parseResampleParams(t, "1m", "4h", "")), "不能整除")
	// 源周期除不尽时段同样拒绝。
	require.ErrorContains(t, ValidateTaskParamsForSource(src, parseResampleParams(t, "45m", "1d", "")), "不能整除")

	for _, pair := range [][2]string{{"1m", "5m"}, {"1m", "1h"}, {"5m", "30m"}, {"15m", "2h"}, {"1m", "1d"}, {"1h", "1d"}} {
		require.NoError(t, ValidateTaskParamsForSource(src, parseResampleParams(t, pair[0], pair[1], "")), "%s→%s", pair[0], pair[1])
	}
}

func TestValidateTaskParamsRejectsUnknownSessionCalendar(t *testing.T) {
	unknownCal := storagesource.DatasetInfo{
		Alignment:   "session",
		Attributes:  map[string]string{"calendar": "jp_stock"},
		ColumnTypes: ohlcvColumnTypes(),
	}
	require.ErrorContains(t, ValidateTaskParamsForSource(unknownCal, parseResampleParams(t, "1m", "5m", "")), "交易日历")
}

// 重采样只允许选择“标准归约规则表 ∩ 源数据集活跃列”内的输出字段：拒绝 close_time、源数据集缺失的可选列与未知字段。
func TestValidateTaskParamsRejectsUnsupportedOutputFields(t *testing.T) {
	cryptoSrc := sourceInfoForSpace("crypto")
	stockSrc := sourceInfoForSpace("stockcn")
	stockSrc.ColumnTypes = stockSelectedColumnTypes()
	ohlcvSrc := storagesource.DatasetInfo{Alignment: "epoch_utc", ColumnTypes: ohlcvColumnTypes()}

	require.NoError(t, ValidateTaskParamsForSource(cryptoSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["open","close","quote_volume","trade_num"]`)))
	require.NoError(t, ValidateTaskParamsForSource(stockSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["open","close","volume","amount"]`)))
	require.NoError(t, ValidateTaskParamsForSource(ohlcvSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["open","high","low","close","volume"]`)))

	require.ErrorContains(t, ValidateTaskParamsForSource(cryptoSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","close_time"]`)), "close_time")
	require.ErrorContains(t, ValidateTaskParamsForSource(cryptoSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","amount"]`)), "amount")
	require.ErrorContains(t, ValidateTaskParamsForSource(stockSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","close_time"]`)), "close_time")
	require.ErrorContains(t, ValidateTaskParamsForSource(stockSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","quote_volume"]`)), "quote_volume")
	require.ErrorContains(t, ValidateTaskParamsForSource(stockSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","trade_num"]`)), "trade_num")
	require.ErrorContains(t, ValidateTaskParamsForSource(ohlcvSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","amount"]`)), "amount")
	require.ErrorContains(t, ValidateTaskParamsForSource(cryptoSrc, parseResampleParams(t, "1m", "5m", `,"output_fields":["close","unknown_col"]`)), "unknown_col")
}
