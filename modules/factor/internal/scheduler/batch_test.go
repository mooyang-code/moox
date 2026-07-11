package scheduler

import (
	"context"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
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
