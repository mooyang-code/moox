package scheduler

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"sync"
	"testing"
	"time"
)

func TestPartitionByCost(t *testing.T) {
	got := Partition([]engine.FactorSpec{{FactorID: "slow"}, {FactorID: "a"}, {FactorID: "b"}}, map[string]int64{"slow": 90, "a": 30, "b": 30}, 2, 50)
	if len(got) != 2 {
		t.Fatalf("batches=%d", len(got))
	}
}

type sharedFrameExecutor struct {
	mu    sync.Mutex
	frame *engine.DataFrame
	calls int
}

func (e *sharedFrameExecutor) Execute(_ context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.frame == nil {
		e.frame = frame
	}
	if e.frame != frame {
		return nil, context.Canceled
	}
	e.calls++
	columns := make(map[string]engine.FactorColumnResult, len(task.Factors))
	for _, spec := range task.Factors {
		columns[spec.Name+"_1"] = engine.FactorColumnResult{Tail: 1, Values: []any{1.0}}
	}
	return &engine.FactorResult{Columns: columns}, nil
}
func (*sharedFrameExecutor) Close() error { return nil }

func TestFactorBatchesShareOneLoadedFrame(t *testing.T) {
	exec := &sharedFrameExecutor{}
	svc := NewService(Config{Workers: 4}, nil, exec)
	specs := make([]engine.FactorSpec, 100)
	for i := range specs {
		specs[i] = engine.FactorSpec{FactorID: string(rune('a'+i%26)) + string(rune('0'+i/26)), Name: "Factor" + string(rune('A'+i%26)) + string(rune('a'+i/26)), Params: []int{1}}
	}
	frame := &engine.DataFrame{Columns: []string{"close"}, Rows: [][]any{{1.0}}, DataTimes: []time.Time{time.Now()}}
	result, err := svc.executeFactorBatches(context.Background(), engine.FactorTask{TaskID: "shared", SubjectID: "BTC", Factors: specs}, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != len(specs) || exec.calls < 2 {
		t.Fatalf("columns=%d calls=%d", len(result.Columns), exec.calls)
	}
}

type countingSnapshotStorage struct {
	reads  atomic.Int32
	writes atomic.Int32
	frame  *engine.DataFrame
}

func (s *countingSnapshotStorage) ReadWindow(context.Context, storageio.WindowKey, int, time.Time, []string) (*engine.DataFrame, error) {
	s.reads.Add(1)
	return s.frame, nil
}
func (s *countingSnapshotStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.DataFrame, *engine.FactorResult) error {
	s.writes.Add(1)
	return nil
}

type snapshotAwareExecutor struct {
	path atomic.Value
}

func (e *snapshotAwareExecutor) Execute(_ context.Context, task *engine.FactorTask, _ *engine.DataFrame) (*engine.FactorResult, error) {
	if task.SnapshotPath == "" {
		return nil, context.Canceled
	}
	if old := e.path.Load(); old != nil && old.(string) != task.SnapshotPath {
		return nil, context.Canceled
	}
	e.path.Store(task.SnapshotPath)
	columns := make(map[string]engine.FactorColumnResult, len(task.Factors))
	for _, spec := range task.Factors {
		columns[spec.Name+"_1"] = engine.FactorColumnResult{Tail: 1, Values: []any{1.0}}
	}
	return &engine.FactorResult{Columns: columns}, nil
}
func (*snapshotAwareExecutor) Close() error { return nil }

func TestParentTaskReadsOnceAndSharesSnapshot(t *testing.T) {
	storage := &countingSnapshotStorage{frame: &engine.DataFrame{Columns: []string{"close"}, Rows: [][]any{{1.0}}, DataTimes: []time.Time{time.Now()}}}
	exec := &snapshotAwareExecutor{}
	svc := NewService(Config{Workers: 8, SnapshotDir: t.TempDir()}, storage, exec)
	specs := make([]engine.FactorSpec, 100)
	for i := range specs {
		specs[i] = engine.FactorSpec{FactorID: fmt.Sprintf("f-%03d", i), Name: fmt.Sprintf("Factor%03d", i), Params: []int{1}}
	}
	svc.Enqueue(context.Background(), Task{FactorTask: engine.FactorTask{TaskID: "parent-100", Kind: "timeseries", SubjectID: "BTC", Freq: "1m", LookbackBars: 1, Factors: specs}})
	if err := svc.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if storage.reads.Load() != 1 || storage.writes.Load() != 1 {
		t.Fatalf("reads=%d writes=%d", storage.reads.Load(), storage.writes.Load())
	}
}

func TestValidateFactorResultRejectsNonFiniteValues(t *testing.T) {
	specs := []engine.FactorSpec{{Name: "Bias", Params: []int{1}}}
	for _, value := range []any{"bad", nil, math.NaN(), math.Inf(1)} {
		err := validateFactorResult(specs, &engine.FactorResult{Columns: map[string]engine.FactorColumnResult{
			"Bias_1": {Tail: 1, Values: []any{value}},
		}})
		if err == nil {
			t.Fatalf("value %#v was accepted", value)
		}
	}
}
