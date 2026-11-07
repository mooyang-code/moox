package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type runningActionSourceStub struct {
	actions []store.OperatorActionRecord
	err     error
}

func (s runningActionSourceStub) ListAllRunningOperatorActions(
	context.Context,
) ([]store.OperatorActionRecord, error) {
	return append([]store.OperatorActionRecord(nil), s.actions...), s.err
}

type actionResumerStub struct {
	mu      sync.Mutex
	resumed []string
	fail    map[string]error
}

func (s *actionResumerStub) ResumeOperatorAction(
	_ context.Context,
	action store.OperatorActionRecord,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumed = append(s.resumed, action.SpaceID+"/"+action.ActionID)
	return s.fail[action.ActionID]
}

func (s *actionResumerStub) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resumed...)
}

func (s *actionResumerStub) setFailure(actionID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.fail, actionID)
		return
	}
	if s.fail == nil {
		s.fail = make(map[string]error)
	}
	s.fail[actionID] = err
}

func TestOperatorWorkerRecoversEveryPersistedRunningAction(t *testing.T) {
	resumer := &actionResumerStub{fail: map[string]error{
		"action-b": errors.New("still unavailable"),
	}}
	worker := &OperatorWorker{
		Actions: runningActionSourceStub{actions: []store.OperatorActionRecord{
			{SpaceID: "space-1", ActionID: "action-a", Status: "RUNNING"},
			{SpaceID: "space-2", ActionID: "action-b", Status: "RUNNING"},
		}},
		Resumer: resumer,
	}

	failures, err := worker.runOnce(context.Background())

	require.ErrorContains(t, err, "still unavailable", "没有判定函数时所有错误都算共享故障")
	require.Equal(t, []OperatorActionFailure{{SpaceID: "space-2", ActionID: "action-b", Error: "still unavailable"}}, failures)
	require.Equal(t, []string{"space-1/action-a", "space-2/action-b"}, resumer.snapshot())
}

func TestOperatorWorkerRejectsMissingDependencies(t *testing.T) {
	require.ErrorIs(t, (&OperatorWorker{}).Run(context.Background()), ErrOperatorWorkerConfig)
}

type deadlineActionResumer struct{ t *testing.T }

func (r deadlineActionResumer) ResumeOperatorAction(ctx context.Context, _ store.OperatorActionRecord) error {
	deadline, ok := ctx.Deadline()
	require.True(r.t, ok, "every action must have a finite worker attempt budget")
	require.LessOrEqual(r.t, time.Until(deadline), 30*time.Second)
	return nil
}

func TestOperatorWorkerBoundsEveryAction(t *testing.T) {
	w := &OperatorWorker{Actions: runningActionSourceStub{actions: []store.OperatorActionRecord{{ActionID: "bounded"}}}, Resumer: deadlineActionResumer{t}}
	failures, err := w.runOnce(context.Background())
	require.NoError(t, err)
	require.Empty(t, failures)
}

func TestOperatorWorkerRunsImmediatelyAndStopsOnCancellation(t *testing.T) {
	resumer := &actionResumerStub{}
	worker := &OperatorWorker{
		Actions: runningActionSourceStub{actions: []store.OperatorActionRecord{{
			SpaceID: "space-1", ActionID: "action-a", Status: "RUNNING",
		}}},
		Resumer: resumer, Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool {
		return len(resumer.snapshot()) == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// 单个动作一直恢复失败、错误只关乎这个动作：失败按动作记下，工作线程照样就绪（不能让生命周期检查失败、自动暂停整个 Trade）；
// 恢复后失败记录清掉。
func TestOperatorWorkerIsolatesActionFailuresFromReadiness(t *testing.T) {
	scoped := errors.New("recovery failed")
	resumer := &actionResumerStub{fail: map[string]error{"action-a": scoped}}
	worker := &OperatorWorker{
		Actions: runningActionSourceStub{actions: []store.OperatorActionRecord{{
			SpaceID: "space-1", ActionID: "action-a", ActionType: "FLATTEN", Status: "RUNNING",
		}}},
		Resumer: resumer, Interval: 5 * time.Millisecond,
		ActionScoped: func(err error) bool { return errors.Is(err, scoped) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return snapshot.Ready && len(snapshot.ActionErrors) == 1 && len(resumer.snapshot()) >= 2
	}, time.Second, time.Millisecond)
	failure := worker.Snapshot().ActionErrors[0]
	require.Equal(t, "action-a", failure.ActionID)
	require.Equal(t, "FLATTEN", failure.ActionType)
	require.Contains(t, failure.Error, "recovery failed")
	require.Empty(t, worker.Snapshot().LastError)
	resumer.setFailure("action-a", nil)
	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return snapshot.Ready && len(snapshot.ActionErrors) == 0
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// 列不出运行中的动作是工作线程自身的故障：不就绪。
func TestOperatorWorkerNotReadyWhenListingFails(t *testing.T) {
	worker := &OperatorWorker{
		Actions: runningActionSourceStub{err: errors.New("database is locked")},
		Resumer: &actionResumerStub{}, Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return !snapshot.Ready && snapshot.LastError == "database is locked"
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}
