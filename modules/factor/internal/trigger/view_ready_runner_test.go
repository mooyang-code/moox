package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestViewReadyRunnerRunsSubjectFactorCartesianProduct(t *testing.T) {
	runner := &blockingCombinationRunner{tasks: make(chan []taskrunner.Task, 1), release: make(chan struct{})}
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	ready := &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m", PeriodTime: period.Unix(), Status: "complete",
		PrimarySubjects: []string{"SOL", "BTC", "ETH"}, ReadyAt: timestamppb.New(period),
	}

	done := make(chan error, 1)
	go func() { done <- executor.Execute(context.Background(), "space", "source-event", ready) }()
	tasks := <-runner.tasks
	got := make([]string, 0, len(tasks))
	for _, task := range tasks {
		got = append(got, task.SubjectID+"/"+task.Factor.FactorID)
		require.Equal(t, taskrunner.DeterministicTaskID(task), task.TaskID)
	}
	require.Equal(t, []string{
		"BTC/bias5", "BTC/bias20", "ETH/bias5", "ETH/bias20", "SOL/bias5", "SOL/bias20",
	}, got)
	require.Nil(t, storage.getMarker(), "marker must wait for every combination to become terminal")

	close(runner.release)
	require.NoError(t, <-done)
	require.Equal(t, "complete", storage.getMarker().GetStatus())
	require.Equal(t, 1, storage.markerCount())
}

func TestViewReadyRunnerExecuteSelectedRunsOnlyRequestedFactor(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 1, 1, 0, 0, time.UTC)
	ready := &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m", PeriodTime: period.Unix(), Status: "complete",
		PrimarySubjects: []string{"BTC", "ETH"}, ReadyAt: timestamppb.New(period),
	}

	require.NoError(t, executor.ExecuteSelected(context.Background(), "space", "recalc-event", "bias5", ready))
	require.Len(t, runner.tasks, 2)
	for _, task := range runner.tasks {
		require.Equal(t, "bias5", task.Factor.FactorID)
	}
	marker := storage.getMarker()
	require.Len(t, marker.GetBindings(), 1)
	require.Equal(t, "bias5", marker.GetBindings()[0].GetFactorId())
}

func TestViewReadyRunnerIsolatesCombinationFailure(t *testing.T) {
	runner := &recordingCombinationRunner{fail: map[string]error{"b-20-bias20\x00ETH": errors.New("python failed")}}
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	err := executor.Execute(context.Background(), "space", "source-event", &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m", PeriodTime: period.Unix(), Status: "complete",
		PrimarySubjects: []string{"SOL", "BTC", "ETH"}, ReadyAt: timestamppb.New(period),
	})
	require.NoError(t, err)
	require.Len(t, runner.tasks, 6)
	require.Equal(t, []string{"b-20-bias20/ETH"}, storage.clearedCombinations())
	marker := storage.getMarker()
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, 1, storage.markerCount())
	require.Equal(t, "b-05-bias5", marker.GetBindings()[0].GetBindingId())
	require.Equal(t, "complete", marker.GetBindings()[0].GetStatus())
	require.Equal(t, "b-20-bias20", marker.GetBindings()[1].GetBindingId())
	require.Equal(t, "degraded", marker.GetBindings()[1].GetStatus())
	require.Equal(t, []string{"ETH"}, marker.GetBindings()[1].GetFailedSubjects())
}

func TestViewReadyRunnerClearsSkippedAndUpstreamFailedWithoutRunningThem(t *testing.T) {
	bindings := periodBindings{rows: []domain.FactorBinding{{
		BindingID: "binding", FactorID: "factor", SpaceID: "space", SourceViewID: "source_view",
		ResultDatasetID: "result", Freq: "1m", SubjectMode: domain.SubjectModeInclude,
		SubjectsJSON: `["BTC"]`, Status: domain.BindingStatusEnabled,
	}}}
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	period := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)
	err := NewViewReadyRunner(bindings, periodFactors{"factor": testFactor("factor")}, runner, storage, t.TempDir()).Execute(
		context.Background(), "space", "source-event", &publicstoragepb.ViewSourcePeriodReady{
			SourceViewId: "source_view", Frequency: "1m", PeriodTime: period.Unix(), Status: "degraded",
			PrimarySubjects: []string{"SOL", "BTC", "ETH"}, ReadyAt: timestamppb.New(period),
			Datasets: []*publicstoragepb.ViewPeriodDatasetState{{DatasetId: "prices", Status: "degraded", FailedSubjects: []string{"BTC"}}},
		})
	require.NoError(t, err)
	require.Empty(t, runner.tasks)
	require.Equal(t, []string{"binding/BTC", "binding/ETH", "binding/SOL"}, storage.clearedCombinations())
	state := storage.getMarker().GetBindings()[0]
	require.Equal(t, []string{"BTC"}, state.GetFailedSubjects())
	require.Equal(t, []string{"ETH", "SOL"}, state.GetSkippedSubjects())
}

func TestViewReadyRunnerMarksBindingDegradedForAuxiliaryFailure(t *testing.T) {
	bindings := periodBindings{rows: []domain.FactorBinding{{BindingID: "binding", FactorID: "factor", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", Status: domain.BindingStatusEnabled}}}
	storage := new(periodStorageFake)
	period := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	err := NewViewReadyRunner(bindings, periodFactors{"factor": testFactor("factor")}, new(recordingCombinationRunner), storage, t.TempDir()).Execute(context.Background(), "space", "source-event", &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m", PeriodTime: period.Unix(), Status: "degraded",
		PrimarySubjects: []string{"BTC"}, Datasets: []*publicstoragepb.ViewPeriodDatasetState{{DatasetId: "aux", Status: "degraded", FailedSubjects: []string{"OTHER"}}}, ReadyAt: timestamppb.New(period),
	})
	require.NoError(t, err)
	require.Equal(t, "degraded", storage.getMarker().GetStatus())
	require.Equal(t, "degraded", storage.getMarker().GetBindings()[0].GetStatus())
}

func TestViewReadyRunnerRealtimeNoBindingIsNoop(t *testing.T) {
	storage := &periodStorageFake{preflightErr: errors.New("result dataset is not found")}
	executor := NewViewReadyRunner(periodBindings{}, periodFactors{}, new(recordingCombinationRunner), storage, t.TempDir())
	ready := &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m", PeriodTime: time.Date(2026, 8, 10, 5, 0, 0, 0, time.UTC).Unix(), Status: "complete",
	}
	require.NoError(t, executor.Execute(context.Background(), "space", "source-event", ready))
	require.Nil(t, storage.getMarker())
	require.ErrorIs(t, executor.ExecuteSelected(context.Background(), "space", "recalc-event", "", ready), ErrNoExecutableBinding)
}

func TestViewReadyRunnerRetriesWhileBindingIsPending(t *testing.T) {
	bindings := pendingPeriodBindings{waiting: true}
	executor := NewViewReadyRunner(bindings, periodFactors{}, new(recordingCombinationRunner), new(periodStorageFake), t.TempDir())
	ready := &publicstoragepb.ViewSourcePeriodReady{
		SourceViewId: "source_view", Frequency: "1m",
		PeriodTime: time.Date(2026, 8, 10, 5, 1, 0, 0, time.UTC).Unix(), Status: "complete",
	}
	require.ErrorIs(t, executor.Execute(context.Background(), "space", "source-event", ready), ErrBindingNotReady)
}

func twoPeriodBindings() periodBindings {
	return periodBindings{rows: []domain.FactorBinding{
		{BindingID: "b-20-bias20", FactorID: "bias20", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", Status: domain.BindingStatusEnabled},
		{BindingID: "b-05-bias5", FactorID: "bias5", SpaceID: "space", SourceViewID: "source_view", ResultDatasetID: "result", Freq: "1m", Status: domain.BindingStatusEnabled},
	}}
}

func twoPeriodFactors() periodFactors {
	return periodFactors{"bias5": testFactor("bias5"), "bias20": testFactor("bias20")}
}

func testFactor(id string) domain.FactorDef {
	return domain.FactorDef{FactorID: id, Name: id, SourceHash: "hash-" + id, InputColumns: []string{"close"}, Outputs: []string{id}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled}
}

type periodBindings struct{ rows []domain.FactorBinding }

func (p periodBindings) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return append([]domain.FactorBinding(nil), p.rows...), nil
}

type pendingPeriodBindings struct{ waiting bool }

func (p pendingPeriodBindings) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return nil, nil
}

func (p pendingPeriodBindings) HasExecutableOrPending(context.Context, string, string, string) (bool, error) {
	return p.waiting, nil
}

type periodFactors map[string]domain.FactorDef

func (p periodFactors) Get(_ context.Context, factorID string) (*domain.FactorDef, error) {
	factor, ok := p[factorID]
	if !ok {
		return nil, fmt.Errorf("factor %s not found", factorID)
	}
	value := factor
	return &value, nil
}

type blockingCombinationRunner struct {
	tasks   chan []taskrunner.Task
	release chan struct{}
}

func (r *blockingCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	copied := append([]taskrunner.Task(nil), tasks...)
	r.tasks <- copied
	<-r.release
	results := make([]taskrunner.Result, len(copied))
	for index := range copied {
		results[index] = taskrunner.Result{Task: copied[index]}
	}
	return results
}

type recordingCombinationRunner struct {
	mu    sync.Mutex
	tasks []taskrunner.Task
	fail  map[string]error
}

func (r *recordingCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	r.mu.Lock()
	r.tasks = append(r.tasks, tasks...)
	r.mu.Unlock()
	results := make([]taskrunner.Result, len(tasks))
	for index, task := range tasks {
		results[index] = taskrunner.Result{Task: task, Err: r.fail[combinationKey(task.BindingID, task.SubjectID)]}
	}
	return results
}

type periodStorageFake struct {
	mu           sync.Mutex
	marker       *storagepb.FactorPeriodComputedMarker
	markers      int
	cleared      []*engine.FactorTask
	preflightErr error
}

func (p *periodStorageFake) FactorPeriodComputed(context.Context, string, string, string, int64) (bool, error) {
	return false, p.preflightErr
}

func (p *periodStorageFake) ReportFactorPeriodComputed(_ context.Context, _ string, marker *storagepb.FactorPeriodComputedMarker) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.marker = marker
	p.markers++
	return nil
}

func (p *periodStorageFake) ClearFactorOutputs(_ context.Context, task *engine.FactorTask) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	copyTask := *task
	p.cleared = append(p.cleared, &copyTask)
	return nil
}

func (p *periodStorageFake) getMarker() *storagepb.FactorPeriodComputedMarker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.marker
}

func (p *periodStorageFake) markerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.markers
}

func (p *periodStorageFake) clearedCombinations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, 0, len(p.cleared))
	for _, task := range p.cleared {
		result = append(result, task.BindingID+"/"+task.SubjectID)
	}
	return result
}
