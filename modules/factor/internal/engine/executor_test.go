package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/stretchr/testify/require"
)

func TestPythonExecutorRunsSingleFactorWithOutputIdentity(t *testing.T) {
	factorsDir := t.TempDir()
	source := []byte(`import pandas as pd

def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    output["double"] = df["value"] * params["multiple"]
    return output
`)
	sourcePath := filepath.Join(factorsDir, "Double.py")
	require.NoError(t, os.WriteFile(sourcePath, source, 0o600))
	hash := sha256.Sum256(source)
	executor, err := NewPythonExecutor(context.Background(), 1, process.Config{
		PythonBin:  "python3",
		WorkerPath: filepath.Clean(filepath.Join("..", "..", "pyworker", "worker.py")),
		Args:       []string{"--factors-dir", factorsDir},
		Limits:     process.DefaultLimits(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	at := time.Date(2026, 7, 29, 0, 0, 0, 1, time.UTC)
	result, err := executor.Execute(context.Background(), &FactorTask{
		TaskID: "executor-test", SubjectID: "BTC-USDT",
		StartTime: at, EndTime: at.Add(time.Nanosecond),
		Factor: FactorSpec{
			FactorID: "double", Name: "Double", SourcePath: sourcePath,
			SourceHash: hex.EncodeToString(hash[:]), InputColumns: []string{"value"},
			Outputs: []string{"double"}, ParamsJSON: `{"multiple":2}`,
		},
	}, &DataFrame{
		Columns: []string{"value"}, Rows: [][]any{{3.0}},
		DataTimes: []time.Time{at}, SeriesTags: []string{"venue:binance"},
	})
	require.NoError(t, err)
	require.Equal(t, []FactorResultRow{{
		DataTime: at, SeriesTag: "venue:binance", Values: map[string]any{"double": 6.0},
	}}, result.Rows)
}
