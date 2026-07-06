package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

func TestHashSubjectKeepsSameSubjectOnSameShard(t *testing.T) {
	a := HashSubject("BTC-USDT", 8)
	b := HashSubject("BTC-USDT", 8)
	if a != b {
		t.Fatalf("same subject got different shards: %d vs %d", a, b)
	}
	if a < 0 || a >= 8 {
		t.Fatalf("shard out of range: %d", a)
	}
}

func TestQueueSupersedesPendingTaskByNewerBarTime(t *testing.T) {
	ctx := context.Background()
	runs := &recordingRuns{}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, &fakeExecutor{}, runs)
	t0 := time.Date(2026, 7, 6, 9, 14, 0, 0, time.UTC)

	svc.Enqueue(ctx, taskAt(t0))
	svc.Enqueue(ctx, taskAt(t0.Add(time.Minute)))
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	if len(runs.rows) != 2 {
		t.Fatalf("run rows = %+v", runs.rows)
	}
	if runs.rows[0].Status != domain.RunStatusSuperseded || runs.rows[0].BarTime != t0.Format(time.RFC3339) {
		t.Fatalf("first row should supersede old task: %+v", runs.rows[0])
	}
	if runs.rows[1].Status != domain.RunStatusSucceeded || runs.rows[1].BarTime != t0.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("second row should execute newest task: %+v", runs.rows[1])
	}
}

func TestQueueKeepsDifferentTargetDatasetsSeparate(t *testing.T) {
	ctx := context.Background()
	runs := &recordingRuns{}
	exec := &fakeExecutor{}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, exec, runs)
	t0 := time.Date(2026, 7, 6, 9, 14, 0, 0, time.UTC)

	first := taskAt(t0)
	second := taskAt(t0.Add(time.Minute))
	second.TargetDataset = "binance_spot_volume_factor"
	svc.Enqueue(ctx, first)
	svc.Enqueue(ctx, second)
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	if exec.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", exec.calls)
	}
	if len(runs.rows) != 2 {
		t.Fatalf("run rows = %+v", runs.rows)
	}
}

func TestRetryableErrorsRetryAtMostMaxRetry(t *testing.T) {
	ctx := context.Background()
	exec := &fakeExecutor{retryFailures: 4}
	runs := &recordingRuns{}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, exec, runs)

	svc.Enqueue(ctx, taskAt(time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)))
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}

	if exec.calls != 4 {
		t.Fatalf("executor calls = %d, want initial + 3 retries", exec.calls)
	}
	if runs.rows[len(runs.rows)-1].Status != domain.RunStatusFailed {
		t.Fatalf("final run = %+v", runs.rows[len(runs.rows)-1])
	}
}

func TestNonRetryableErrorRecordsFailureAndNextTaskContinues(t *testing.T) {
	ctx := context.Background()
	exec := &fakeExecutor{nonRetryableOnce: true}
	runs := &recordingRuns{}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, exec, runs)
	t0 := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)

	svc.Enqueue(ctx, taskAt(t0))
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain(first) error = %v", err)
	}
	svc.Enqueue(ctx, taskAt(t0.Add(time.Minute)))
	if err := svc.Drain(ctx); err != nil {
		t.Fatalf("Drain(second) error = %v", err)
	}

	if len(runs.rows) != 2 {
		t.Fatalf("run rows = %+v", runs.rows)
	}
	if runs.rows[0].Status != domain.RunStatusFailed {
		t.Fatalf("first row = %+v", runs.rows[0])
	}
	if runs.rows[1].Status != domain.RunStatusSucceeded {
		t.Fatalf("second row = %+v", runs.rows[1])
	}
}

func TestDrainStopsBeforeNextTaskWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := &cancelOnFirstExecutor{cancel: cancel}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, exec, &recordingRuns{})
	t0 := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)

	first := taskAt(t0)
	second := taskAt(t0.Add(time.Minute))
	second.TargetDataset = "binance_spot_volume_factor"
	svc.Enqueue(ctx, first)
	svc.Enqueue(ctx, second)

	if err := svc.Drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Drain() error = %v, want context.Canceled", err)
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("executor calls before cancellation = %d, want 1", exec.calls.Load())
	}
	if err := svc.Drain(context.Background()); err != nil {
		t.Fatalf("Drain(background) error = %v", err)
	}
	if exec.calls.Load() != 2 {
		t.Fatalf("executor calls after background drain = %d, want 2", exec.calls.Load())
	}
}

func TestConcurrentDrainWaitRespectsContext(t *testing.T) {
	ctx := context.Background()
	exec := &blockingExecutor{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	svc := NewService(Config{Workers: 1, MaxRetry: 3}, &fakeStorage{}, exec, &recordingRuns{})
	svc.Enqueue(ctx, taskAt(time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)))

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- svc.Drain(ctx)
	}()
	select {
	case <-exec.started:
	case <-time.After(time.Second):
		t.Fatal("first Drain did not start executing")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := svc.Drain(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent Drain() error = %v, want context deadline", err)
	}
	close(exec.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Drain() error = %v", err)
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("executor calls = %d, want 1", exec.calls.Load())
	}
}

func taskAt(barTime time.Time) Task {
	return Task{
		FactorTask: engine.FactorTask{
			TaskID:        "task-" + barTime.Format("1504"),
			Kind:          "timeseries",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			TargetDataset: "binance_spot_factor",
			SubjectID:     "BTC-USDT",
			Freq:          "1m",
			BarTime:       barTime,
			LookbackBars:  200,
			Factors: []engine.FactorSpec{
				{FactorID: "bias", Name: "Bias", Params: []int{20}, WritebackBars: 5},
			},
		},
		TriggerType: "event",
	}
}

type fakeStorage struct{}

func (f *fakeStorage) ReadWindow(context.Context, storageio.WindowKey, int, time.Time, []string) (*engine.DataFrame, error) {
	return &engine.DataFrame{DataTimes: []time.Time{time.Now().UTC()}}, nil
}

func (f *fakeStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.DataFrame, *engine.FactorResult) error {
	return nil
}

type fakeExecutor struct {
	calls            int
	retryFailures    int
	nonRetryableOnce bool
}

func (f *fakeExecutor) Execute(context.Context, *engine.FactorTask, *engine.DataFrame) (*engine.FactorResult, error) {
	f.calls++
	if f.nonRetryableOnce {
		f.nonRetryableOnce = false
		return nil, engine.NonRetryableError{Err: errors.New("factor failed")}
	}
	if f.retryFailures > 0 {
		f.retryFailures--
		return nil, engine.RetryableError{Err: errors.New("worker crashed")}
	}
	return &engine.FactorResult{ElapsedMS: 7, Columns: map[string]engine.FactorColumnResult{}}, nil
}

func (f *fakeExecutor) Close() error { return nil }

type cancelOnFirstExecutor struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (f *cancelOnFirstExecutor) Execute(context.Context, *engine.FactorTask, *engine.DataFrame) (*engine.FactorResult, error) {
	if f.calls.Add(1) == 1 {
		f.cancel()
	}
	return &engine.FactorResult{ElapsedMS: 7, Columns: map[string]engine.FactorColumnResult{}}, nil
}

func (f *cancelOnFirstExecutor) Close() error { return nil }

type blockingExecutor struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (f *blockingExecutor) Execute(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	f.calls.Add(1)
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return &engine.FactorResult{ElapsedMS: 7, Columns: map[string]engine.FactorColumnResult{}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *blockingExecutor) Close() error { return nil }

type recordingRuns struct {
	rows []domain.FactorRun
}

func (r *recordingRuns) Insert(_ context.Context, run domain.FactorRun) error {
	r.rows = append(r.rows, run)
	return nil
}
