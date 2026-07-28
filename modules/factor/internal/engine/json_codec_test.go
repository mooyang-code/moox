package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeJSONRequestMetaBuildsRangePayload(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 1, time.UTC)
	task := &FactorTask{
		TaskID: "t1", StartTime: start, EndTime: start.Add(time.Minute),
		Factors: []FactorSpec{{
			FactorID: "f1", Name: "Bias", InputColumns: []string{"close"},
			Outputs: []string{"bias_20"}, ParamsJSON: `{"windows":[20]}`,
		}},
	}
	frame := &DataFrame{
		Columns: []string{"close"}, Rows: [][]any{{1.2}}, DataTimes: []time.Time{start},
	}
	meta, err := EncodeJSONRequestMeta(task, frame)
	require.NoError(t, err)
	require.Equal(t, "json", meta["encoding"])
	require.Equal(t, start.Format(time.RFC3339Nano), meta["target_start_time"])
	factors := meta["factors"].([]map[string]any)
	require.Equal(t, []string{"close"}, factors[0]["input_columns"])
	require.Equal(t, []string{"bias_20"}, factors[0]["outputs"])
	require.Equal(t, map[string]any{"windows": []any{float64(20)}}, factors[0]["params"])
	df := meta["df"].(map[string]any)
	require.Equal(t, []string{"2026-01-02T03:00:00.000000001Z"}, df["data_times"])
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
