package trigger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestRecalcTaskDuplicateRequestIsIdempotent(t *testing.T) {
	svc, exec := openRecalcService(t)
	first, err := svc.Accept(context.Background(), testRecalcSpec("req-1"))
	require.NoError(t, err)
	require.Equal(t, RecalcAccepted, first.Status)
	second, err := svc.Accept(context.Background(), testRecalcSpec("req-1"))
	require.NoError(t, err)
	require.Equal(t, first.JobID, second.JobID)
	require.Equal(t, RecalcAccepted, second.Status)
	require.NoError(t, svc.Execute(context.Background(), first.JobID))
	require.Equal(t, 1, exec.runs)
}

func TestRecalcTaskCancelDoesNotSucceed(t *testing.T) {
	svc, exec := openRecalcService(t)
	job, err := svc.Accept(context.Background(), testRecalcSpec("req-cancel"))
	require.NoError(t, err)
	require.NoError(t, svc.Cancel(context.Background(), job.JobID))
	require.NoError(t, svc.Execute(context.Background(), job.JobID))
	got, err := svc.Get(context.Background(), job.JobID)
	require.NoError(t, err)
	require.Equal(t, RecalcCancelled, got.Status)
	require.Zero(t, exec.runs)
	require.Zero(t, exec.successes)
}

func TestRecalcTaskDisabledBindingDoesNotPolluteNewOutput(t *testing.T) {
	svc, exec := openRecalcService(t)
	spec := testRecalcSpec("req-gen")
	spec.BindingGeneration = "gen-1"
	job, err := svc.Accept(context.Background(), spec)
	require.NoError(t, err)
	exec.currentGeneration = "gen-2"
	require.NoError(t, svc.Execute(context.Background(), job.JobID))
	got, err := svc.Get(context.Background(), job.JobID)
	require.NoError(t, err)
	require.Equal(t, RecalcFailed, got.Status)
	require.Equal(t, RecalcFailureAlgorithm, got.FailureClass)
	require.Empty(t, exec.writtenGenerations)
}

func TestRecalcTaskEngineOfflineAcceptsWithoutCompute(t *testing.T) {
	svc, exec := openRecalcService(t)
	exec.offline = true
	job, err := svc.Accept(context.Background(), testRecalcSpec("req-offline"))
	require.NoError(t, err)
	require.Equal(t, RecalcAccepted, job.Status)
	require.Zero(t, exec.runs)
	got, err := svc.Get(context.Background(), job.JobID)
	require.NoError(t, err)
	require.Equal(t, RecalcAccepted, got.Status)
}

func TestRecalcTaskClaimNextIsExclusive(t *testing.T) {
	svc, _ := openRecalcService(t)
	_, err := svc.Accept(context.Background(), testRecalcSpec("req-claim"))
	require.NoError(t, err)
	first, ok, err := svc.ClaimNext(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, RecalcRunning, first.Status)
	_, ok, err = svc.ClaimNext(context.Background())
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRecalcTaskHeartbeatSurfacesDesiredApplied(t *testing.T) {
	svc, _ := openRecalcService(t)
	require.NoError(t, svc.Heartbeat(context.Background(), "factor-engine-1", 9, 8))
	got, err := svc.LatestHeartbeat(context.Background())
	require.NoError(t, err)
	require.Equal(t, "factor-engine-1", got.EngineID)
	require.EqualValues(t, 9, got.DesiredRevision)
	require.EqualValues(t, 8, got.AppliedRevision)
	require.NotNil(t, got.LastSeen)
}

func TestRecalcTaskQueueReportsIndependentCompletion(t *testing.T) {
	bus := testkit.Start(t)
	svc, _ := openRecalcService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nc, err := nats.Connect(bus.URL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })
	subs, err := ServeRecalcQueue(ctx, nc, svc)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	})
	accepted, err := svc.Accept(ctx, testRecalcSpec("req-bus"))
	require.NoError(t, err)
	require.NoError(t, ReportRecalcHeartbeat(ctx, nc, "factor-engine-1", 3, 3))
	job, found, err := ClaimRecalcJob(ctx, nc)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accepted.JobID, job.JobID)
	require.NoError(t, ReportRecalcJob(ctx, nc, job.JobID, RecalcSucceeded, "", "recalc-batch"))
	got, err := svc.Get(ctx, job.JobID)
	require.NoError(t, err)
	require.Equal(t, RecalcSucceeded, got.Status)
	heartbeat, err := svc.LatestHeartbeat(ctx)
	require.NoError(t, err)
	require.Equal(t, "factor-engine-1", heartbeat.EngineID)
	require.EqualValues(t, 3, heartbeat.DesiredRevision)
}

func TestRecalcTaskStatusDistinguishesFailures(t *testing.T) {
	svc, exec := openRecalcService(t)
	cases := []struct {
		requestID string
		runErr    error
		class     string
	}{
		{"req-missing", ErrRecalcMissingInput, RecalcFailureMissingInput},
		{"req-algo", errors.New("python failed"), RecalcFailureAlgorithm},
		{"req-view", ErrRecalcViewWaiting, RecalcFailureViewWaiting},
	}
	for _, tc := range cases {
		exec.runErr = tc.runErr
		job, err := svc.Accept(context.Background(), testRecalcSpec(tc.requestID))
		require.NoError(t, err)
		require.NoError(t, svc.Execute(context.Background(), job.JobID))
		got, err := svc.Get(context.Background(), job.JobID)
		require.NoError(t, err)
		require.Equal(t, RecalcFailed, got.Status)
		require.Equal(t, tc.class, got.FailureClass, tc.requestID)
	}
}

func openRecalcService(t *testing.T) (*RecalcService, *recordingRecalcExecutor) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	exec := &recordingRecalcExecutor{currentGeneration: "gen-1"}
	svc, err := NewRecalcService(db, exec)
	require.NoError(t, err)
	return svc, exec
}

func testRecalcSpec(requestID string) RecalcSpec {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	return RecalcSpec{
		RequestID: requestID, SpaceID: "space", DatasetID: "mdataset_binance_kline_1m",
		SourceViewID: "source_view", SubjectID: "BTC-USDT", Frequency: "1m", FactorID: "bias5",
		BindingID: "bind-bias5", BindingGeneration: "gen-1",
		StartTime: start, EndTime: start.Add(time.Minute),
	}
}

type recordingRecalcExecutor struct {
	offline            bool
	runErr             error
	currentGeneration  string
	runs               int
	successes          int
	writtenGenerations []string
}

func (e *recordingRecalcExecutor) Run(_ context.Context, job RecalcJob) error {
	e.runs++
	if e.offline {
		return ErrRecalcEngineOffline
	}
	if e.runErr != nil {
		return e.runErr
	}
	if e.currentGeneration != "" && job.BindingGeneration != e.currentGeneration {
		return errors.New("stale binding generation")
	}
	e.successes++
	e.writtenGenerations = append(e.writtenGenerations, job.BindingGeneration)
	return nil
}
