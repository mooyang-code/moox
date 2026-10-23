package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func parseResampleParams(t *testing.T, source, target string, overrides string) *domain.CollectParams {
	t.Helper()
	raw := `{"provider":"moox","market_type":"spot","source_dataset_id":"dataset_source","source_frequency":"` + source + `","source_series_tag":"venue:binance","target_dataset_id":"dataset_target","target_frequency":"` + target + `"` + overrides + `}`
	params, err := domain.ParseCollectParams(raw, "", "", "kline_resample")
	require.NoError(t, err)
	return params
}

func TestValidateTaskParamsAcceptsTaskOwnedTargetWithoutFrequencySuffix(t *testing.T) {
	// 结果 Dataset ID 由任务派生，不带频率后缀；这是启动时清单刷新曾因后缀检查失败的场景。
	require.NoError(t, ValidateTaskParams("crypto", parseResampleParams(t, "1h", "4h", "")))
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
		{name: "wrong alignment for crypto", source: "1m", target: "5m", overrides: `,"alignment":"session"`, want: "alignment must be epoch_utc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, ValidateTaskParams("crypto", parseResampleParams(t, tt.source, tt.target, tt.overrides)), tt.want)
		})
	}
}

func TestValidateTaskParamsCryptoAcceptsNonStandardDivisorsOfOneDay(t *testing.T) {
	for _, pair := range [][2]string{{"5m", "45m"}, {"15m", "45m"}, {"1m", "45m"}, {"5m", "2h"}, {"1h", "12h"}, {"15m", "1d"}} {
		require.NoError(t, ValidateTaskParams("crypto", parseResampleParams(t, pair[0], pair[1], "")), "%s→%s", pair[0], pair[1])
	}
	require.NoError(t, ValidateTaskParams("crypto", parseResampleParams(t, "5m", "45m", `,"alignment":"epoch_utc"`)))
}

func TestValidateTaskParamsStockSessionMarketRejectsPeriodsThatDoNotDivideSessions(t *testing.T) {
	err := ValidateTaskParams("stockcn", parseResampleParams(t, "1m", "45m", ""))
	require.ErrorContains(t, err, "09:30–11:30")
	require.ErrorContains(t, err, "可选的日内周期")
	require.ErrorContains(t, ValidateTaskParams("stockcn", parseResampleParams(t, "1m", "4h", "")), "不能整除")
	// 源周期除不尽时段同样拒绝。
	require.ErrorContains(t, ValidateTaskParams("stockcn", parseResampleParams(t, "45m", "1d", "")), "不能整除")

	for _, pair := range [][2]string{{"1m", "5m"}, {"1m", "1h"}, {"5m", "30m"}, {"15m", "2h"}, {"1m", "1d"}, {"1h", "1d"}} {
		require.NoError(t, ValidateTaskParams("stockcn", parseResampleParams(t, pair[0], pair[1], "")), "%s→%s", pair[0], pair[1])
	}
	require.NoError(t, ValidateTaskParams("stockcn", parseResampleParams(t, "1m", "1h", `,"alignment":"session"`)))
	require.ErrorContains(t, ValidateTaskParams("stockcn", parseResampleParams(t, "1m", "1h", `,"alignment":"epoch_utc"`)), "alignment must be session")
}

func TestValidateTaskParamsRejectsMarketsWithoutCalendar(t *testing.T) {
	require.ErrorContains(t, ValidateTaskParams("stockhk", parseResampleParams(t, "1m", "5m", "")), "没有登记交易日历")
}
