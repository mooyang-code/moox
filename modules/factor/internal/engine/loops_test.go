package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/stretchr/testify/require"
)

type heartbeatClientFake struct {
	errs     []error
	statuses []domain.EngineStatus
}

func (f *heartbeatClientFake) Heartbeat(_ context.Context, status domain.EngineStatus) (time.Duration, error) {
	f.statuses = append(f.statuses, status)
	err := f.errs[0]
	f.errs = f.errs[1:]
	return 45 * time.Second, err
}

func TestHeartbeatLeaseConflictStopsAndRecovers(t *testing.T) {
	lease := &leaseState{}
	changes := 0
	client := &heartbeatClientFake{errs: []error{
		nil, fmt.Errorf("%w: held by factor-engine@other", ErrLeaseConflict), errors.New("dial tcp: timeout"), nil,
	}}
	beat := &heartbeater{client: client, lease: lease, status: func() domain.EngineStatus {
		return domain.EngineStatus{CatalogHash: "hash-1", PythonWorkers: 8}
	}, onChange: func() { changes++ }, now: time.Now}

	require.NoError(t, beat.BeatOnce(context.Background()))
	require.False(t, lease.Conflict())

	require.Error(t, beat.BeatOnce(context.Background()))
	require.True(t, lease.Conflict(), "an explicit conflict stops the engine")

	require.Error(t, beat.BeatOnce(context.Background()))
	require.True(t, lease.Conflict(), "an unreachable manager keeps the last lease state")

	require.NoError(t, beat.BeatOnce(context.Background()))
	require.False(t, lease.Conflict())
	require.Equal(t, 2, changes)
	require.Equal(t, "hash-1", client.statuses[0].CatalogHash)
}

func TestHeartbeatUnreachableManagerKeepsRunning(t *testing.T) {
	lease := &leaseState{}
	beat := &heartbeater{client: &heartbeatClientFake{errs: []error{errors.New("connection refused")}}, lease: lease,
		status: func() domain.EngineStatus { return domain.EngineStatus{} }, now: time.Now}

	require.Error(t, beat.BeatOnce(context.Background()))

	require.False(t, lease.Conflict())
	require.Equal(t, "connection refused", lease.snapshot().LastError)
}

type reportCall struct {
	progress time.Time
	status   string
	errText  string
}

type jobClientFake struct {
	job      PulledJob
	found    bool
	pulled   int
	reports  []reportCall
	statuses []string
	err      error
}

func (f *jobClientFake) PullRecalcJob(context.Context) (PulledJob, bool, error) {
	f.pulled++
	found := f.found
	f.found = false
	return f.job, found, nil
}

func (f *jobClientFake) ReportRecalcProgress(_ context.Context, _, _ string, progress time.Time, status, errText string) (string, error) {
	f.reports = append(f.reports, reportCall{progress: progress, status: status, errText: errText})
	if f.err != nil {
		return "", f.err
	}
	if len(f.statuses) > 0 {
		next := f.statuses[0]
		f.statuses = f.statuses[1:]
		return next, nil
	}
	return status, nil
}

type sourceFake struct {
	subjects []string
	listed   int
}

func (f *sourceFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (f *sourceFake) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	f.listed++
	return f.subjects, nil
}

type planRecorder struct {
	plans []pipeline.Plan
	err   error
	out   pipeline.Outcome
}

func (r *planRecorder) Run(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.plans = append(r.plans, plan)
	return r.out, r.err
}

func testJob(subjects ...string) PulledJob {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	return PulledJob{
		JobID: "job-1", LeaseToken: "token", Subjects: subjects,
		Window: recalcexec.Window{Start: start, End: start.Add(6 * time.Minute), Progress: start},
		Set:    engineSet("fset_a", "dataset_a", true, "bias"),
	}
}

func newTestLoop(client *jobClientFake, runner *planRecorder, source *sourceFake) *recalcLoop {
	executor := recalcexec.NewExecutor(runner, recalcexec.WithChunkPeriods(2), recalcexec.WithChunkRetry(1, time.Millisecond))
	return &recalcLoop{client: client, executor: executor, source: source, poll: time.Millisecond}
}

func TestRecalcLoopPullsRunsAndReportsSucceeded(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, runner.plans, 3)
	require.Len(t, client.reports, 3)
	require.Equal(t, jobStatusRunning, client.reports[0].status)
	require.Equal(t, jobStatusSucceeded, client.reports[2].status)
	require.Equal(t, client.job.Window.End, client.reports[2].progress)
	require.Equal(t, []string{"BTC"}, runner.plans[0].Expected)
}

func TestRecalcLoopStopsOnCancelled(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, statuses: []string{"cancelled"}}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, runner.plans, 1)
	require.Len(t, client.reports, 1)
}

func TestRecalcLoopAbandonsOnConflict(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, err: fmt.Errorf("%w: stale token", ErrLeaseConflict)}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, client.reports, 1, "no failed report after losing the lease")
}

func TestRecalcLoopAbandonsWhenManagerUnreachable(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true, err: errors.New("dial tcp: i/o timeout")}
	loop := newTestLoop(client, &planRecorder{out: pipeline.Outcome{Status: "complete"}}, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, client.reports, 1, "the job is left to expire and resume, never marked failed")
}

func TestRecalcLoopReportsFailedWithError(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	runner := &planRecorder{err: errors.New("python factor raised")}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Len(t, client.reports, 1)
	require.Equal(t, jobStatusFailed, client.reports[0].status)
	require.Contains(t, client.reports[0].errText, "python factor raised")
}

func TestRecalcLoopResolvesSubjectsWhenEmpty(t *testing.T) {
	client := &jobClientFake{job: testJob(), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	source := &sourceFake{subjects: []string{"BTC", "ETH"}}
	loop := newTestLoop(client, runner, source)

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Positive(t, source.listed)
	require.Equal(t, []string{"BTC", "ETH"}, runner.plans[0].Expected)
}

func TestRecalcLoopKeepsDegradedNote(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC", "ETH"), found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "degraded", FailedSubjects: []string{"ETH"}}}
	loop := newTestLoop(client, runner, &sourceFake{})

	_, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, "degraded: failed_subjects=1", client.reports[2].errText)
}

func TestRecalcLoopPausesWhileInactive(t *testing.T) {
	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := newTestLoop(client, &planRecorder{}, &sourceFake{})
	loop.active = func() bool { return false }

	found, err := loop.RunOnce(context.Background())

	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.pulled)
}
