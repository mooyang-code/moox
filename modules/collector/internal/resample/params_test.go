package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func parseResampleParams(t *testing.T, overrides string) *domain.CollectParams {
	t.Helper()
	raw := `{"provider":"moox","market_type":"spot","source_dataset_id":"dataset_source","source_frequency":"1h","source_series_tag":"venue:binance","target_dataset_id":"dataset_target","target_frequency":"4h","alignment":"epoch_utc"` + overrides + `}`
	params, err := domain.ParseCollectParams(raw, "", "", "kline_resample")
	require.NoError(t, err)
	return params
}

func TestValidateTaskParamsAcceptsTaskOwnedTargetWithoutFrequencySuffix(t *testing.T) {
	// 结果 Dataset ID 由任务派生，不带频率后缀；这是启动时清单刷新曾因后缀检查失败的场景。
	require.NoError(t, ValidateTaskParams(parseResampleParams(t, "")))
}

func TestValidateTaskParamsRejectsInvalidPairAndIdentity(t *testing.T) {
	tests := []struct {
		name      string
		overrides string
		want      string
	}{
		{name: "same dataset", overrides: `,"source_dataset_id":"dataset_same","target_dataset_id":"dataset_same"`, want: "must differ"},
		{name: "missing series", overrides: `,"source_series_tag":""`, want: "source_series_tag"},
		{name: "calendar target", overrides: `,"source_frequency":"1h","target_frequency":"1w"`, want: "cannot be resampled"},
		{name: "target not larger", overrides: `,"source_frequency":"1h","target_frequency":"1h"`, want: "greater"},
		{name: "bad dataset id", overrides: `,"target_dataset_id":"target"`, want: "dataset_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// JSON 里同名键后出现者覆盖前者。
			require.ErrorContains(t, ValidateTaskParams(parseResampleParams(t, tt.overrides)), tt.want)
		})
	}
}
