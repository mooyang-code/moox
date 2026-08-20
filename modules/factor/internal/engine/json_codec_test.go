package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeJSONRequestMetaBuildsIdentityPayload(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 1, time.UTC)
	task := &FactorTask{
		TaskID: "t1", StartTime: start, EndTime: start.Add(time.Minute),
		Factor: FactorSpec{
			FactorID: "f1", Name: "Bias", InputColumns: []string{"close"},
			Outputs:    []string{"bias_20"},
			ParamsJSON: `{"windows":[20],"large":9007199254740993,"huge":1e400}`,
		},
	}
	frame := &DataFrame{
		Columns: []string{"close"}, Rows: [][]any{{1.2}},
		DataTimes: []time.Time{start}, SeriesTags: []string{"venue:binance"},
	}
	meta, err := EncodeJSONRequestMeta(task, frame)
	require.NoError(t, err)
	require.Equal(t, "json", meta["encoding"])
	require.Equal(t, start.Format(time.RFC3339Nano), meta["target_start_time"])
	factor := meta["factor"].(map[string]any)
	require.Equal(t, []string{"close"}, factor["input_columns"])
	require.Equal(t, []string{"bias_20"}, factor["outputs"])
	require.Equal(t, map[string]any{
		"windows": []any{json.Number("20")},
		"large":   json.Number("9007199254740993"),
		"huge":    json.Number("1e400"),
	}, factor["params"])
	encoded, err := json.Marshal(meta)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"large":9007199254740993`)
	require.Contains(t, string(encoded), `"huge":1e400`)
	df := meta["df"].(map[string]any)
	require.Equal(t, []string{"data_time", "series_tag", "close"}, df["columns"])
	require.Equal(t, [][]any{{
		"2026-01-02T03:00:00.000000001Z", "venue:binance", 1.2,
	}}, df["rows"])
}

func TestEncodeJSONRequestMetaRejectsMisalignedIdentity(t *testing.T) {
	_, err := EncodeJSONRequestMeta(
		&FactorTask{Factor: FactorSpec{FactorID: "f", ParamsJSON: `{}`}},
		&DataFrame{Rows: [][]any{{}}, DataTimes: []time.Time{time.Now()}},
	)
	require.ErrorContains(t, err, "identities")
}

func TestDecodeJSONResponseUsesCompleteRowIdentity(t *testing.T) {
	at := "2026-01-02T03:00:00.000000001Z"
	result, err := DecodeJSONResponse(map[string]any{
		"results": []any{
			map[string]any{
				"data_time": at, "series_tag": "venue_pair:binance-okx",
				"values": map[string]any{"spread": 1.5, "rank": nil},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	require.Equal(t, "venue_pair:binance-okx", result.Rows[0].SeriesTag)
	require.Equal(t, map[string]any{"spread": 1.5, "rank": nil}, result.Rows[0].Values)
	require.Equal(t, at, result.Rows[0].DataTime.Format(time.RFC3339Nano))

	for _, meta := range []map[string]any{
		{},
		{"results": map[string]any{}},
		{"results": []any{map[string]any{
			"data_time": "bad", "series_tag": "", "values": map[string]any{},
		}}},
		{"results": []any{map[string]any{
			"data_time": at, "series_tag": " bad", "values": map[string]any{},
		}}},
	} {
		_, err = DecodeJSONResponse(meta)
		require.Error(t, err)
	}
}

func TestDecodeJSONResponseAllowsEmptyDataFrame(t *testing.T) {
	result, err := DecodeJSONResponse(map[string]any{"results": []any{}})
	require.NoError(t, err)
	require.Empty(t, result.Rows)
}

func TestBatchJSONCodecPreservesMemberIdentityAndPartialErrors(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	batch := &BatchTask{BatchID: "b1", Tasks: []FactorTask{
		{TaskID: "t1", BindingID: "bind-1", SpaceID: "crypto", SourceViewID: "bars", ResultDatasetID: "bias", SubjectID: "BTC", Freq: "1m", StartTime: start, EndTime: start.Add(time.Minute), LookbackPeriods: 20, Factor: FactorSpec{FactorID: "f1", Name: "Bias", ParamsJSON: `{}`}},
		{TaskID: "t2", BindingID: "bind-2", SpaceID: "crypto", SourceViewID: "bars", ResultDatasetID: "cci", SubjectID: "BTC", Freq: "1m", StartTime: start.Add(time.Minute), EndTime: start.Add(2 * time.Minute), LookbackPeriods: 600, Factor: FactorSpec{FactorID: "f2", Name: "CCI", ParamsJSON: `{}`}},
	}}
	meta, err := EncodeJSONBatchRequestMeta(batch, &DataFrame{Columns: []string{"close"}, Rows: [][]any{{1.0}}, DataTimes: []time.Time{start}, SeriesTags: []string{"venue:binance"}})
	require.NoError(t, err)
	require.Equal(t, "batch", meta["mode"])
	require.Len(t, meta["factors"], 2)
	first := meta["factors"].([]any)[0].(map[string]any)
	second := meta["factors"].([]any)[1].(map[string]any)
	require.Equal(t, 20, first["lookback_periods"])
	require.Equal(t, 600, second["lookback_periods"])

	decoded, err := DecodeJSONBatchResponse(map[string]any{
		"id": "b1", "items": []any{
			map[string]any{"task_id": "t1", "binding_id": "bind-1", "ok": true, "results": []any{}},
			map[string]any{"task_id": "t2", "binding_id": "bind-2", "ok": false, "message": "factor failed"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "b1", decoded.BatchID)
	require.Len(t, decoded.Items, 2)
	require.NoError(t, decoded.Items[0].Err)
	require.Empty(t, decoded.Items[0].Result.Rows)
	require.ErrorContains(t, decoded.Items[1].Err, "factor failed")
}

func TestDecodeJSONBatchResponseRejectsDuplicateTask(t *testing.T) {
	_, err := DecodeJSONBatchResponse(map[string]any{"items": []any{
		map[string]any{"task_id": "t1", "binding_id": "b1", "ok": true, "results": []any{}},
		map[string]any{"task_id": "t1", "binding_id": "b2", "ok": true, "results": []any{}},
	}})
	require.ErrorContains(t, err, "duplicate")
}
