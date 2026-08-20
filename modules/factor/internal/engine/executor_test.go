package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	poolpkg "github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/stretchr/testify/require"
)

func TestPythonWorkerPoolRunsSingleFactorWithOutputIdentity(t *testing.T) {
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
	executor, err := NewPythonWorkerPool(context.Background(), 1, process.Config{
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

func TestPythonWorkerPoolBatchKeepsLoadFailureOnOneMember(t *testing.T) {
	factorsDir := t.TempDir()
	goodSource := []byte(`import pandas as pd

def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    output["double"] = df["value"] * 2
    return output
`)
	goodPath := filepath.Join(factorsDir, "Good.py")
	badPath := filepath.Join(factorsDir, "Broken.py")
	require.NoError(t, os.WriteFile(goodPath, goodSource, 0o600))
	require.NoError(t, os.WriteFile(badPath, []byte("def compute(:\n"), 0o600))
	goodHash := sha256.Sum256(goodSource)
	badSource, err := os.ReadFile(badPath)
	require.NoError(t, err)
	badHash := sha256.Sum256(badSource)
	executor, err := NewPythonWorkerPool(context.Background(), 1, process.Config{
		PythonBin:  "python3",
		WorkerPath: filepath.Clean(filepath.Join("..", "..", "pyworker", "worker.py")),
		Args:       []string{"--factors-dir", factorsDir},
		Limits:     process.DefaultLimits(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })
	at := time.Date(2026, 7, 29, 0, 0, 0, 1, time.UTC)
	result, err := executor.ExecuteBatch(context.Background(), &BatchTask{
		BatchID: "batch-load-test",
		Tasks: []FactorTask{
			{TaskID: "good", BindingID: "binding-good", SpaceID: "crypto", SourceViewID: "bars", ResultDatasetID: "good-out", SubjectID: "BTC", Freq: "1m", StartTime: at, EndTime: at.Add(time.Nanosecond), Factor: FactorSpec{FactorID: "good", Name: "Good", SourcePath: goodPath, SourceHash: hex.EncodeToString(goodHash[:]), InputColumns: []string{"value"}, Outputs: []string{"double"}, ParamsJSON: `{}`}},
			{TaskID: "bad", BindingID: "binding-bad", SpaceID: "crypto", SourceViewID: "bars", ResultDatasetID: "bad-out", SubjectID: "BTC", Freq: "1m", StartTime: at, EndTime: at.Add(time.Nanosecond), Factor: FactorSpec{FactorID: "bad", Name: "Broken", SourcePath: badPath, SourceHash: hex.EncodeToString(badHash[:]), InputColumns: []string{"value"}, Outputs: []string{"double"}, ParamsJSON: `{}`}},
		},
	}, &DataFrame{Columns: []string{"value"}, Rows: [][]any{{3.0}}, DataTimes: []time.Time{at}, SeriesTags: []string{"venue:binance"}})
	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	require.Equal(t, "good", result.Items[0].TaskID)
	require.NoError(t, result.Items[0].Err)
	require.Len(t, result.Items[0].Result.Rows, 1)
	require.Equal(t, "bad", result.Items[1].TaskID)
	require.Error(t, result.Items[1].Err)
}

func TestBatchTimeoutScalesWithFactorCount(t *testing.T) {
	require.Equal(t, 30*time.Second, batchTimeout(30*time.Second, 1))
	require.Equal(t, 90*time.Second, batchTimeout(30*time.Second, 3))
	require.Equal(t, 30*time.Second, batchTimeout(0, 0))
}

type blockingExecutorWorker struct {
	entered chan<- string
	release <-chan struct{}
	closed  bool
}

func (*blockingExecutorWorker) Load(context.Context, process.LoadRequest) error { return nil }
func (w *blockingExecutorWorker) Run(_ context.Context, req process.RunRequest) (process.RunResult, error) {
	w.entered <- req.RequestID
	<-w.release
	return process.RunResult{Meta: []byte(`{"results":[]}`)}, nil
}
func (*blockingExecutorWorker) State() process.State { return process.StateReady }
func (w *blockingExecutorWorker) Close() error {
	w.closed = true
	return nil
}

func TestPythonWorkerPoolDispatchesSameSubjectToFreeWorkers(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	workers := []*blockingExecutorWorker{
		{entered: entered, release: release},
		{entered: entered, release: release},
	}
	var factoryMu sync.Mutex
	next := 0
	workerPool := poolpkg.New(2, func(context.Context) (process.Worker, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		worker := workers[next]
		next++
		return worker, nil
	})
	executor := &PythonWorkerPool{
		workers: 2,
		pool:    workerPool,
		hello: protocol.Hello{
			WorkerVersion: "test-worker",
			PythonVersion: "test-python",
		},
	}

	errCh := make(chan error, 2)
	for _, taskID := range []string{"bias-5", "bias-20"} {
		go func(taskID string) {
			_, err := executor.Execute(context.Background(), &FactorTask{
				TaskID: taskID, SubjectID: "BTC-USDT",
				Factor: FactorSpec{FactorID: taskID, ParamsJSON: "{}"},
			}, &DataFrame{})
			errCh <- err
		}(taskID)
	}

	require.ElementsMatch(t, []string{"bias-5", "bias-20"}, []string{<-entered, <-entered})
	status := executor.Status()
	require.Equal(t, 2, status.Workers)
	require.True(t, status.Ready)
	close(release)
	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
	require.NoError(t, executor.Close())
	require.True(t, workers[0].closed)
	require.True(t, workers[1].closed)
}
