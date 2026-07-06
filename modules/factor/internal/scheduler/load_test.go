package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/testkit"
)

func TestSchedulerLoadDrainsSyntheticEventStorm(t *testing.T) {
	ctx := context.Background()
	storage := &testkit.FakeStorage{}
	exec := &testkit.FakeExecutor{Latency: 5 * time.Millisecond}
	runs := &recordingRuns{}
	svc := NewService(Config{Workers: 8, MaxRetry: 1}, storage, exec, runs)
	barTime := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	for i, symbol := range testkit.Symbols(120) {
		task := taskAt(barTime)
		task.TaskID = fmt.Sprintf("task-%d", i)
		task.SubjectID = symbol
		task.Factors = []engine.FactorSpec{{FactorID: "bias", Name: "Bias", Params: []int{20}, WritebackBars: 1}}
		svc.Enqueue(ctx, task)
	}
	started := time.Now()
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("drain took %s, want <= 2s", elapsed)
	}
	if exec.Calls.Load() != 120 || storage.Writes.Load() != 120 {
		t.Fatalf("calls/writes = %d/%d, want 120/120", exec.Calls.Load(), storage.Writes.Load())
	}
}
