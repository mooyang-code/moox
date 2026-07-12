package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeJSONRequestMetaRequiresInputs(t *testing.T) {
	_, err := EncodeJSONRequestMeta(nil, &DataFrame{})
	require.Error(t, err)
	_, err = EncodeJSONRequestMeta(&FactorTask{}, nil)
	require.Error(t, err)
}

func TestEncodeJSONRequestMetaBuildsPayload(t *testing.T) {
	task := &FactorTask{
		TaskID: "t1", SnapshotID: "s1", Kind: "batch", SpaceID: "crypto",
		SourceDataset: "src", TargetDataset: "dst", SubjectID: "BTC", Freq: "1m",
		BarTime: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC),
		Factors: []FactorSpec{{FactorID: "f1", Name: "alpha", WritebackBars: 2}},
	}
	frame := &DataFrame{
		Columns:   []string{"close"},
		Rows:      [][]any{{1.2}},
		DataTimes: []time.Time{task.BarTime},
	}
	meta, err := EncodeJSONRequestMeta(task, frame)
	require.NoError(t, err)
	assert.Equal(t, "json", meta["encoding"])
	df := meta["df"].(map[string]any)
	columns := df["columns"].(map[string][]any)
	assert.Equal(t, []any{1.2}, columns["close"])
}

func TestEncodeJSONRequestMetaUsesArrowMmapWhenSnapshotPathSet(t *testing.T) {
	meta, err := EncodeJSONRequestMeta(&FactorTask{SnapshotPath: "/tmp/snap"}, &DataFrame{})
	require.NoError(t, err)
	assert.Equal(t, "arrow_mmap", meta["encoding"])
	df := meta["df"].(map[string]any)
	assert.Empty(t, df["columns"])
}

func TestDecodeJSONResponse(t *testing.T) {
	meta := map[string]any{
		"elapsed_ms": float64(12),
		"per_factor_ms": map[string]any{"f1": float64(5)},
		"results": map[string]any{
			"alpha": map[string]any{"tail": float64(2), "values": []any{1.0, 2.0}},
		},
	}
	result, err := DecodeJSONResponse(meta)
	require.NoError(t, err)
	assert.Equal(t, int64(12), result.ElapsedMS)
	require.Contains(t, result.Columns, "alpha")
	assert.Equal(t, 2, result.Columns["alpha"].Tail)
	_, err = DecodeJSONResponse(map[string]any{})
	require.Error(t, err)
}

func TestNumberToInt64(t *testing.T) {
	assert.Equal(t, int64(7), numberToInt64(int64(7)))
	assert.Equal(t, int64(3), numberToInt64(3))
	assert.Equal(t, int64(9), numberToInt64(float64(9.9)))
	assert.Equal(t, int64(0), numberToInt64("bad"))
}

func TestDecodeInt64Map(t *testing.T) {
	got := decodeInt64Map(map[string]any{"a": float64(1), "b": int64(2)})
	assert.Equal(t, map[string]int64{"a": 1, "b": 2}, got)
}
