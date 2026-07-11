package e2e_test

import (
	"context"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"path/filepath"
	"testing"
	"time"
)

type storageFake struct{ writes int }

func (s *storageFake) ReadWindow(context.Context, storageio.WindowKey, int, time.Time, []string) (*engine.DataFrame, error) {
	return &engine.DataFrame{Columns: []string{"close"}, Rows: [][]any{{100.0}, {101.0}}, DataTimes: []time.Time{time.Now().UTC().Add(-time.Minute), time.Now().UTC()}}, nil
}
func (s *storageFake) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.DataFrame, *engine.FactorResult) error {
	s.writes++
	return nil
}
func TestFactorSchedulerRunsPythonFactor(t *testing.T) {
	root := ".."
	pool, err := engine.NewRuntimePoolExecutor(context.Background(), 2, process.Config{PythonBin: "python3", WorkerPath: filepath.Join(root, "pyworker", "worker.py"), Args: []string{"--factors-dir", filepath.Join(root, "factors"), "--sections-dir", filepath.Join(root, "sections"), "--encoding", "json"}, Limits: process.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	storage := &storageFake{}
	s := scheduler.NewService(scheduler.Config{Workers: 1}, storage, pool)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	done := make(chan scheduler.TaskResult, 1)
	s.Enqueue(context.Background(), scheduler.Task{FactorTask: engine.FactorTask{TaskID: "e2e-factor", Kind: "timeseries", SpaceID: "s", SourceDataset: "kline", TargetDataset: "factor", SubjectID: "BTC-USDT", Freq: "1m", BarTime: time.Now().UTC(), LookbackBars: 2, Factors: []engine.FactorSpec{{FactorID: "bias", Name: "Bias", SourcePath: filepath.Join(root, "factors", "Bias.py"), Params: []int{2}, WritebackBars: 1}}}, Completion: done})
	if err := s.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Status != "succeeded" {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("factor task did not complete")
	}
	if storage.writes != 1 {
		t.Fatalf("writes=%d", storage.writes)
	}
}
