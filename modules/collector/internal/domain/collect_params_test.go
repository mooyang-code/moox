package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectParams_ParseCollectParams_EmptyJSON_ShouldApplyDefaults(t *testing.T) {
	params, err := ParseCollectParams("", "binance", "kline")
	require.NoError(t, err)
	assert.Equal(t, "dataset_subjects", params.Source.Kind)
	assert.Equal(t, "binance", params.Collector.Exchange)
	assert.Equal(t, "spot", params.Collector.Market)
	assert.Equal(t, "kline", params.Collector.DataType)
	assert.Equal(t, []string{"1m"}, params.Collector.Intervals)
	assert.Equal(t, "binance_spot_kline", params.Source.DatasetID)
	assert.Equal(t, "collect.kline", params.Target.JobType)
	raw, err := json.Marshal(params.Target)
	require.NoError(t, err)
	assert.JSONEq(t, `{"dataset_id":"binance_spot_kline","job_type":"collect.kline"}`, string(raw))
}

func TestCollectParams_ParseCollectParams_SymbolDataType_ShouldUseNoneSource(t *testing.T) {
	raw := `{"collector":{"exchange":"okx","data_type":"symbol"}}`
	params, err := ParseCollectParams(raw, "binance", "symbol")
	require.NoError(t, err)
	assert.Equal(t, "none", params.Source.Kind)
	assert.Equal(t, "okx", params.Collector.Exchange)
	assert.Empty(t, params.Collector.Intervals)
}

func TestCollectParams_ParseCollectParams_InvalidJSON_ShouldReturnError(t *testing.T) {
	_, err := ParseCollectParams("{", "binance", "kline")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse collect params")
}

func TestCollectParams_Normalize_ScheduleIntervals_ShouldCopyToCollectorIntervals(t *testing.T) {
	params := &CollectParams{
		Schedule: CollectSchedule{Intervals: []string{"5m", "15m"}},
	}
	params.Normalize("binance", "kline")
	assert.Equal(t, []string{"5m", "15m"}, params.Collector.Intervals)
}

func TestCollectParams_Normalize_CustomTargetDataset_ShouldPreserveValue(t *testing.T) {
	params := &CollectParams{
		Target: CollectTarget{DatasetID: "custom_dataset"},
	}
	params.Normalize("binance", "kline")
	assert.Equal(t, "custom_dataset", params.Target.DatasetID)
}
