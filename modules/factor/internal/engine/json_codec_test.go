package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeJSONRequestMetaBuildsRangePayload(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	task := &FactorTask{
		TaskID: "t1", StartTime: start, EndTime: start.Add(time.Minute),
		Factors: []FactorSpec{{FactorID: "f1", Name: "Bias", Periods: []int{20}}},
	}
	frame := &DataFrame{
		Columns: []string{"close"}, Rows: [][]any{{1.2}}, DataTimes: []time.Time{start},
	}
	meta, err := EncodeJSONRequestMeta(task, frame)
	require.NoError(t, err)
	require.Equal(t, "json", meta["encoding"])
	require.Equal(t, start.Format(time.RFC3339Nano), meta["target_start_time"])
	factors := meta["factors"].([]map[string]any)
	require.Equal(t, []int{20}, factors[0]["periods"])
}

func TestDecodeJSONResponseUsesPlainArrays(t *testing.T) {
	result, err := DecodeJSONResponse(map[string]any{
		"results": map[string]any{"Bias_20": []any{nil, 1.5}},
	})
	require.NoError(t, err)
	require.Equal(t, []any{nil, 1.5}, result.Columns["Bias_20"])
	_, err = DecodeJSONResponse(map[string]any{})
	require.Error(t, err)
}
