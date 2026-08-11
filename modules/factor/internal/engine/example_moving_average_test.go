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

func TestMovingAverageExampleFactors(t *testing.T) {
	factorsDir := filepath.Clean(filepath.Join("..", "..", "..", "..", "examples", "factors"))
	executor, err := NewPythonWorkerPool(context.Background(), 1, process.Config{
		PythonBin:  "python3",
		WorkerPath: filepath.Clean(filepath.Join("..", "..", "pyworker", "worker.py")),
		Args:       []string{"--factors-dir", factorsDir},
		Limits:     process.DefaultLimits(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	frame := &DataFrame{
		Columns:    []string{"close"},
		Rows:       [][]any{{1.0}, {2.0}, {3.0}, {4.0}},
		DataTimes:  []time.Time{start, start.Add(time.Minute), start.Add(2 * time.Minute), start.Add(3 * time.Minute)},
		SeriesTags: []string{"venue:binance", "venue:binance", "venue:binance", "venue:binance"},
	}

	for _, test := range []struct {
		factorID string
		file     string
		output   string
		params   string
		expected []float64
	}{
		{factorID: "ma", file: "timeseries/ma.py", output: "ma_3", params: `{"windows":[3]}`, expected: []float64{1, 1.5, 2, 3}},
		{factorID: "sma", file: "timeseries/sma.py", output: "sma_3", params: `{"windows":[3],"m":1}`, expected: []float64{1, 4.0 / 3.0, 17.0 / 9.0, 70.0 / 27.0}},
	} {
		t.Run(test.factorID, func(t *testing.T) {
			sourcePath := filepath.Join(factorsDir, filepath.FromSlash(test.file))
			source, readErr := os.ReadFile(sourcePath)
			require.NoError(t, readErr)
			hash := sha256.Sum256(source)
			result, runErr := executor.Execute(context.Background(), &FactorTask{
				TaskID: "example-" + test.factorID, SubjectID: "BTC-USDT",
				StartTime: start, EndTime: start.Add(4 * time.Minute),
				Factor: FactorSpec{
					FactorID: test.factorID, Name: test.factorID, SourcePath: sourcePath,
					SourceHash: hex.EncodeToString(hash[:]), InputColumns: []string{"close"},
					Outputs: []string{test.output}, ParamsJSON: test.params,
				},
			}, frame)
			require.NoError(t, runErr)
			require.Len(t, result.Rows, len(test.expected))
			for index, expected := range test.expected {
				require.InDelta(t, expected, result.Rows[index].Values[test.output], 1e-12)
			}
		})
	}

	t.Run("sma20_600_period_warmup", func(t *testing.T) {
		sourcePath := filepath.Join(factorsDir, "timeseries", "sma.py")
		source, readErr := os.ReadFile(sourcePath)
		require.NoError(t, readErr)
		hash := sha256.Sum256(source)
		run := func(taskID string, values []float64) float64 {
			rows := make([][]any, len(values))
			times := make([]time.Time, len(values))
			tags := make([]string, len(values))
			for index, value := range values {
				rows[index] = []any{value}
				times[index] = start.Add(time.Duration(index) * time.Minute)
				tags[index] = "venue:binance"
			}
			result, runErr := executor.Execute(context.Background(), &FactorTask{
				TaskID: taskID, SubjectID: "BTC-USDT", StartTime: times[0], EndTime: times[len(times)-1].Add(time.Minute),
				Factor: FactorSpec{
					FactorID: "sma", Name: "sma", SourcePath: sourcePath,
					SourceHash: hex.EncodeToString(hash[:]), InputColumns: []string{"close"},
					Outputs: []string{"sma_20"}, ParamsJSON: `{"windows":[20],"m":1}`,
				},
			}, &DataFrame{Columns: []string{"close"}, Rows: rows, DataTimes: times, SeriesTags: tags})
			require.NoError(t, runErr)
			value, ok := result.Rows[len(result.Rows)-1].Values["sma_20"].(float64)
			require.True(t, ok)
			return value
		}

		full := append([]float64{100}, make([]float64, 600)...)
		warmed := make([]float64, 600)
		require.InDelta(t, run("sma-full", full), run("sma-warmed", warmed), 1e-9)
	})
}
