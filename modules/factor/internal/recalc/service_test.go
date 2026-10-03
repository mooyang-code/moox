package recalc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

type recordingRunner struct {
	mu       sync.Mutex
	plans    []pipeline.Plan
	onRun    func(context.Context, pipeline.Plan) (pipeline.Outcome, error)
	progress []int64
	db       *store.Store
	jobID    string
}

func (r *recordingRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	r.mu.Unlock()
	if r.db != nil {
		job, err := r.db.GetRecalcJob(ctx, r.jobID)
		if err != nil {
			return pipeline.Outcome{}, err
		}
		r.mu.Lock()
		r.progress = append(r.progress, job.ProgressTime)
		r.mu.Unlock()
	}
	if r.onRun != nil {
		return r.onRun(ctx, plan)
	}
	return pipeline.Outcome{Status: "complete"}, nil
}

func (r *recordingRunner) snapshot() []pipeline.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.Plan(nil), r.plans...)
}

func (r *recordingRunner) progressSnapshot() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.progress...)
}

func TestSubmitIsIdempotentByRequestID(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.FactorStatusEnabled, 3),
	)
	svc := NewService(db, &recordingRunner{}, WithClock(periodclock.Continuous{}))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	one, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	two, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	require.Equal(t, one, two)
}

func TestRunSplitsIntoChunksAndAdvancesProgress(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.FactorStatusEnabled, 3),
	)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(5000 * time.Minute)
	runner := &recordingRunner{db: db, jobID: "req-1"}
	svc := NewService(db, runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2000))
	_, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, end)
	require.NoError(t, err)
	require.NoError(t, svc.RunJob(context.Background(), "req-1"))

	plans := runner.snapshot()
	require.Len(t, plans, 3)
	require.Equal(t, start, plans[0].TargetStart)
	require.Equal(t, start.Add(2000*time.Minute), plans[0].TargetEnd)
	require.Equal(t, start.Add(2000*time.Minute), plans[1].TargetStart)
	require.Equal(t, start.Add(4000*time.Minute), plans[2].TargetStart)
	require.Equal(t, []int64{start.Unix(), start.Add(2000 * time.Minute).Unix(), start.Add(4000 * time.Minute).Unix()}, runner.progressSnapshot())
	job, err := svc.Get(context.Background(), "req-1")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusSucceeded, job.Status)
	require.Equal(t, end.Unix(), job.ProgressTime)
}

func TestRecalcUsesSelectedFactorsOnly(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("one", domain.FactorStatusEnabled, 2),
		testRecalcFactor("two", domain.FactorStatusEnabled, 5),
	)
	runner := &recordingRunner{}
	svc := NewService(db, runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(10))
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	_, err := svc.Submit(context.Background(), "fset_bars_1m", []string{"two"}, nil, "req-1", start, start.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, svc.RunJob(context.Background(), "req-1"))
	plans := runner.snapshot()
	require.Len(t, plans, 1)
	require.Equal(t, []string{"two"}, []string{plans[0].Factors[0].FactorID})
	require.Equal(t, pipeline.ModeRecalc, plans[0].Mode)
	require.True(t, plans[0].WriteCarry)
}

func TestCancelStopsBeforeNextChunk(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.FactorStatusEnabled, 1),
	)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	entered := make(chan struct{})
	finish := make(chan struct{})
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		close(entered)
		<-finish
		return pipeline.Outcome{Status: "complete"}, nil
	}}
	svc := NewService(db, runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(1))
	_, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, start.Add(3*time.Minute))
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- svc.RunJob(context.Background(), "req-1") }()
	<-entered
	job, err := svc.Cancel(context.Background(), "req-1")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusCancelled, job.Status)
	close(finish)
	require.NoError(t, <-runDone)
	require.Len(t, runner.snapshot(), 1)
}

func TestChunkHoldsSetLock(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.FactorStatusEnabled, 1),
	)
	locks := &catalog.Locks{}
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	entered := make(chan struct{})
	finish := make(chan struct{})
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		close(entered)
		<-finish
		return pipeline.Outcome{Status: "complete"}, nil
	}}
	svc := NewService(db, runner, WithClock(periodclock.Continuous{}), WithLocks(locks))
	_, err := svc.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, start.Add(time.Minute))
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- svc.RunJob(context.Background(), "req-1") }()
	<-entered
	lockAcquired := make(chan struct{})
	go func() {
		unlock := locks.Lock("fset_bars_1m")
		close(lockAcquired)
		unlock()
	}()
	select {
	case <-lockAcquired:
		t.Fatal("set lock was released during chunk execution")
	case <-time.After(25 * time.Millisecond):
	}
	close(finish)
	require.NoError(t, <-runDone)
	select {
	case <-lockAcquired:
	case <-time.After(time.Second):
		t.Fatal("set lock was not released after chunk execution")
	}
}

func TestRunningJobsResumeAfterRestart(t *testing.T) {
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("close_factor", domain.FactorStatusEnabled, 4),
	)
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	resumeAt := start.Add(2 * time.Minute)
	_, err := db.CreateRecalcJob(context.Background(), store.RecalcJob{
		JobID: "req-1", RequestID: "req-1", SetID: "fset_bars_1m", FactorIDs: []string{"close_factor"},
		Subjects: []string{"BTC"}, StartTime: start.Unix(), EndTime: start.Add(4 * time.Minute).Unix(),
		Status: store.RecalcStatusRunning, ProgressTime: resumeAt.Unix(),
	})
	require.NoError(t, err)
	runner := &recordingRunner{}
	svc := NewService(db, runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(1))
	require.NoError(t, svc.RunPending(context.Background()))
	plans := runner.snapshot()
	require.Len(t, plans, 2)
	require.Equal(t, resumeAt, plans[0].TargetStart)
	job, err := svc.Get(context.Background(), "req-1")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusSucceeded, job.Status)
	require.Equal(t, start.Add(4*time.Minute).Unix(), job.ProgressTime)
}

func TestChunkReadRangeIncludesLookback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	db := openRecalcStore(t)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"},
		testRecalcFactor("short", domain.FactorStatusEnabled, 2),
		testRecalcFactor("long", domain.FactorStatusEnabled, 3),
	)
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, &recalcExecutor{}, periodclock.Continuous{}, pipeline.Config{PythonWorkers: 1})
	service := NewService(db, runner,
		WithClock(periodclock.Continuous{}),
		WithColumnProvider(storage),
		WithChunkPeriods(3),
	)
	_, err := service.Submit(context.Background(), "fset_bars_1m", nil, nil, "req-1", start, start.Add(3*time.Minute))
	require.NoError(t, err)
	err = service.RunJob(context.Background(), "req-1")
	require.NoError(t, err)
	require.Len(t, storage.readRequests, 1)
	require.Equal(t, start.Add(-2*time.Minute), storage.readRequests[0].Start)
	require.Equal(t, start.Add(3*time.Minute), storage.readRequests[0].End)
	require.Len(t, storage.written, 3)
	require.Equal(t, start, storage.written[0].DataTime)
	require.Equal(t, 7.0, storage.written[0].Fields["long_value"])
	require.Equal(t, 7.0, storage.written[0].Fields["short_value"])
}

func TestRecalcDoesNotReportMarker(t *testing.T) {
	worker, storage := recalcPipelineWorker(t)
	_, err := worker.RunOnce(context.Background(), "fset_bars_1m", nil, nil, time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 0, storage.markerCalls)
}

func recalcPipelineWorker(t *testing.T) (*Worker, *recalcPipelineStore) {
	t.Helper()
	db := openRecalcStore(t)
	factor := testRecalcFactor("close_factor", domain.FactorStatusEnabled, 3)
	seedRecalcSet(t, db, domain.SubjectModeInclude, []string{"BTC"}, factor)
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, &recalcExecutor{}, periodclock.Continuous{}, pipeline.Config{PythonWorkers: 1})
	worker := NewWorker(db, runner, WithClock(periodclock.Continuous{}), WithColumnProvider(storage))
	return worker, storage
}

type recalcPipelineStore struct {
	readRequests []storageio.ReadRequest
	markerCalls  int
	written      []storageio.ResultRow
}

func (*recalcPipelineStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (s *recalcPipelineStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	s.readRequests = append(s.readRequests, req)
	frame := &storageio.Frame{SubjectID: "BTC", Columns: []string{"data_time", "series_tag", "close"}}
	for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
		frame.Rows = append(frame.Rows, []any{at, "source", 7.0})
	}
	return map[string]*storageio.Frame{"BTC": frame}, nil
}

func (s *recalcPipelineStore) WriteRows(_ context.Context, _, _, _ string, rows []storageio.ResultRow) error {
	s.written = append(s.written, rows...)
	return nil
}

func (s *recalcPipelineStore) ReportComputed(context.Context, storageio.PeriodMarker) error {
	s.markerCalls++
	return nil
}

func (*recalcPipelineStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

type recalcExecutor struct{}

func (*recalcExecutor) Exec(_ context.Context, req pyexec.Request) ([]pyexec.ItemResult, error) {
	targets, ok := req.Context["target_period_times"].([]string)
	if !ok {
		return nil, errors.New("recalc target periods are missing")
	}
	items := make([]pyexec.ItemResult, 0, len(req.Factors))
	for _, factor := range req.Factors {
		rows := make([][]any, 0, len(targets))
		for _, raw := range targets {
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, err
			}
			rows = append(rows, []any{at, "source", 7.0})
		}
		items = append(items, pyexec.ItemResult{FactorID: factor.FactorID, Columns: []string{"data_time", "series_tag", factor.Outputs[0]}, Rows: rows})
	}
	return items, nil
}

func (*recalcExecutor) Busy() int    { return 0 }
func (*recalcExecutor) Close() error { return nil }

func openRecalcStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: t.TempDir() + "/factor.db"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func seedRecalcSet(t *testing.T, db *store.Store, mode string, subjects []string, factors ...domain.FactorDef) {
	t.Helper()
	set := domain.FactorSet{
		SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "dataset_bars_1m",
		Freq: "1m", SubjectMode: mode, Subjects: subjects,
		ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
	}
	require.NoError(t, db.CreateSet(context.Background(), set))
	for _, factor := range factors {
		factor.SetID = set.SetID
		require.NoError(t, db.CreateFactor(context.Background(), factor))
	}
}

func testRecalcFactor(id, status string, lookback int) domain.FactorDef {
	return domain.FactorDef{
		FactorID: id, SetID: "fset_bars_1m", Name: id, FactorType: domain.FactorTypeTimeSeries,
		SourceCode: "def compute(df, params, context): return df", SourceHash: domain.SourceHash("def compute(df, params, context): return df"),
		InputColumns: []string{"close"}, Outputs: []string{id + "_value"}, ParamsJSON: "{}",
		LookbackPeriods: lookback, Status: status,
	}
}
