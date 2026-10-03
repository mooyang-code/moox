package taskrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/stretchr/testify/require"
)

func TestRunAllBatchMapsMergedColumnsForPythonWorker(t *testing.T) {
	factorsDir := t.TempDir()
	closePath, closeHash := writeMappedFactor(t, factorsDir, "CloseFactor", "close")
	volumePath, volumeHash := writeMappedFactor(t, factorsDir, "VolumeFactor", "volume")
	executor, err := engine.NewPythonWorkerPool(context.Background(), 1, process.Config{
		PythonBin:  "python3",
		WorkerPath: filepath.Clean(filepath.Join("..", "..", "pyworker", "worker.py")),
		Args:       []string{"--factors-dir", factorsDir},
		Limits:     process.DefaultLimits(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, executor.Close()) })

	base := time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC)
	first := oneBarTask("BTC-USDT", base)
	first.PeriodTime = base.Unix()
	first.TaskID = "task-close"
	first.BindingID = "binding-close"
	first.SourceDataset = "mdataset_binance_kline_1m"
	first.PreferredSourceDataset = "dataset_binance_spot_kline_1m"
	first.Factor = engine.FactorSpec{
		FactorType: "timeseries", FactorID: "close-factor", Name: "CloseFactor",
		SourcePath: closePath, SourceHash: closeHash, InputColumns: []string{"close"},
		Outputs: []string{"double"}, ParamsJSON: `{}`,
	}
	second := first
	second.TaskID = "task-volume"
	second.BindingID = "binding-volume"
	second.Factor = engine.FactorSpec{
		FactorType: "timeseries", FactorID: "volume-factor", Name: "VolumeFactor",
		SourcePath: volumePath, SourceHash: volumeHash, InputColumns: []string{"volume"},
		Outputs: []string{"double"}, ParamsJSON: `{}`,
	}
	storage := &mappedBatchStorage{}
	runner := NewService(2, storage, executor)

	results := runner.RunAll(context.Background(), []Task{first, second})

	require.Len(t, results, 2)
	require.NoError(t, results[0].Err)
	require.NoError(t, results[1].Err)
	require.Equal(t, 20.0, storage.value("close-factor"))
	require.Equal(t, 6.0, storage.value("volume-factor"))
	require.Equal(t, []string{
		"dataset_binance_spot_kline_1m__close", "dataset_binance_spot_kline_1m__volume",
	}, storage.readColumns())
}

func writeMappedFactor(t *testing.T, dir, name, input string) (string, string) {
	t.Helper()
	source := []byte("def compute(df, params, context):\n" +
		"    result = df[[\"data_time\", \"series_tag\"]].copy()\n" +
		"    result[\"double\"] = df[\"" + input + "\"] * 2\n" +
		"    return result\n")
	path := filepath.Join(dir, name+".py")
	require.NoError(t, os.WriteFile(path, source, 0o600))
	hash := sha256.Sum256(source)
	return path, hex.EncodeToString(hash[:])
}

type mappedBatchStorage struct {
	mu      sync.Mutex
	columns []string
	values  map[string]float64
}

func (*mappedBatchStorage) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	return nil, errors.New("unexpected range read")
}

func (s *mappedBatchStorage) ReadPeriodChunk(_ context.Context, _ storageio.WindowKey, start time.Time, _ time.Time, _ int, columns []string) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	s.columns = append([]string(nil), columns...)
	s.mu.Unlock()
	row := make([]any, len(columns))
	for index, column := range columns {
		switch {
		case strings.HasSuffix(column, "__close"):
			row[index] = 10.0
		case strings.HasSuffix(column, "__volume"):
			row[index] = 3.0
		default:
			return nil, errors.New("unexpected physical input column: " + column)
		}
	}
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: columns, Rows: [][]any{row}, DataTimes: []time.Time{start},
			SeriesTags: []string{"venue:binance"}, SubjectIDs: []string{"BTC-USDT"},
		},
		TargetPeriods: []time.Time{start}, Complete: true,
	}, nil
}

func (s *mappedBatchStorage) WriteFactorPatch(_ context.Context, task *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	if task == nil || result == nil || len(result.Rows) != 1 {
		return 0, errors.New("expected one factor output row")
	}
	value, ok := result.Rows[0].Values["double"].(float64)
	if !ok {
		return 0, errors.New("factor result is missing double output")
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = make(map[string]float64)
	}
	s.values[task.Factor.FactorID] = value
	s.mu.Unlock()
	return 1, nil
}

func (s *mappedBatchStorage) readColumns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.columns...)
}

func (s *mappedBatchStorage) value(factorID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[factorID]
}
