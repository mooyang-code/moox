package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFrameCodecLayoutAndRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	meta := map[string]any{"id": "task-1", "encoding": "json"}
	payload := []byte("payload")

	if err := WriteFrame(&buf, FrameTypeRequest, meta, payload); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	raw := buf.Bytes()
	wantPrefix := []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0}
	if !bytes.Equal(raw[:6], wantPrefix) {
		t.Fatalf("frame prefix = % x", raw[:6])
	}

	frame, err := ReadFrame(&buf, 1024)
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if frame.Type != FrameTypeRequest || !reflect.DeepEqual(frame.Meta, meta) || string(frame.Payload) != "payload" {
		t.Fatalf("frame = %+v", frame)
	}
}

func TestReadFrameRejectsCorruptTruncatedAndOversizedFrames(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "corrupt magic", raw: []byte("NO")},
		{name: "truncated meta", raw: []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0, 4, '{'}},
		{name: "truncated payload", raw: []byte{0x4d, 0x58, byte(FrameTypeRequest), 0, 0, 0, 2, '{', '}', 0, 0, 0, 0, 0, 0, 0, 4, 'x'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ReadFrame(bytes.NewReader(tt.raw), 1024); err == nil {
				t.Fatal("ReadFrame() error = nil")
			}
		})
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameTypeRequest, map[string]any{"id": "too-big"}, bytes.Repeat([]byte("x"), 8)); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if _, err := ReadFrame(&buf, 4); err == nil {
		t.Fatal("ReadFrame() oversized error = nil")
	}
}

func TestEncodeJSONRequestMetaUsesColumnarDataFrame(t *testing.T) {
	tm0 := time.Date(2026, 7, 6, 9, 14, 0, 0, time.UTC)
	tm1 := tm0.Add(time.Minute)
	task := &FactorTask{
		TaskID:        "task-1",
		Kind:          "timeseries",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		TargetDataset: "binance_spot_factor",
		SubjectID:     "BTC-USDT",
		Freq:          "1m",
		BarTime:       tm1,
		Factors: []FactorSpec{
			{FactorID: "bias", Name: "Bias", Params: []int{20}, WritebackBars: 5},
		},
	}
	frame := &DataFrame{
		Columns:   []string{"open", "close"},
		Rows:      [][]any{{1.0, 2.0}, {3.0, nil}},
		DataTimes: []time.Time{tm0, tm1},
	}

	meta, err := EncodeJSONRequestMeta(task, frame)
	if err != nil {
		t.Fatalf("EncodeJSONRequestMeta() error = %v", err)
	}
	df := meta["df"].(map[string]any)
	columns := df["columns"].(map[string][]any)
	if !reflect.DeepEqual(columns["open"], []any{1.0, 3.0}) {
		t.Fatalf("open column = %#v", columns["open"])
	}
	if !reflect.DeepEqual(columns["close"], []any{2.0, nil}) {
		t.Fatalf("close column = %#v", columns["close"])
	}
	if !reflect.DeepEqual(df["index_ms"], []int64{tm0.UnixMilli(), tm1.UnixMilli()}) {
		t.Fatalf("index_ms = %#v", df["index_ms"])
	}
}

func TestStdioExecutorExecutesPythonWorker(t *testing.T) {
	root := findModuleRoot(t)
	factorsDir := t.TempDir()
	sectionsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte(
		"def signal_multi_params(df, param_list):\n"+
			"    return {str(p): df['close'] + int(p) for p in param_list}\n",
	), 0o644); err != nil {
		t.Fatalf("write factor: %v", err)
	}
	exec, err := NewStdioExecutor(StdioConfig{
		PythonBin:     "python3",
		WorkerPath:    filepath.Join(root, "pyworker", "worker.py"),
		FactorsDir:    factorsDir,
		SectionsDir:   sectionsDir,
		Encoding:      "json",
		TaskTimeout:   5 * time.Second,
		MaxFrameBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewStdioExecutor() error = %v", err)
	}
	defer exec.Close()

	result, err := exec.Execute(context.Background(), &FactorTask{
		TaskID: "task-1",
		Kind:   "timeseries",
		Factors: []FactorSpec{
			{FactorID: "bias", Name: "Bias", Params: []int{2}, WritebackBars: 2},
		},
	}, &DataFrame{
		Columns:   []string{"close"},
		Rows:      [][]any{{1.0}, {2.0}, {3.0}},
		DataTimes: []time.Time{time.Unix(0, 0), time.Unix(60, 0), time.Unix(120, 0)},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := result.Columns["Bias_2"].Values
	if !reflect.DeepEqual(got, []any{4.0, 5.0}) {
		t.Fatalf("Bias_2 values = %#v", got)
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "pyworker", "worker.py")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("module root not found")
		}
		dir = next
	}
}
