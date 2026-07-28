package engine

import (
	"encoding/json"
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
			Outputs:    []string{"bias_20"},
			ParamsJSON: `{"windows":[20],"large":9007199254740993,"huge":1e400}`,
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
	require.Equal(t, map[string]any{
		"windows": []any{json.Number("20")},
		"large":   json.Number("9007199254740993"),
		"huge":    json.Number("1e400"),
	}, factors[0]["params"])
	encoded, err := json.Marshal(meta)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"large":9007199254740993`)
	require.Contains(t, string(encoded), `"huge":1e400`)
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
