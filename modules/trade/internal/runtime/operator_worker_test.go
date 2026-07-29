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

	err := worker.runOnce(context.Background())

	require.ErrorContains(t, err, "still unavailable")
	require.Equal(t, []string{"space-1/action-a", "space-2/action-b"}, resumer.snapshot())
}

func TestOperatorWorkerRejectsMissingDependencies(t *testing.T) {
	require.ErrorIs(t, (&OperatorWorker{}).Run(context.Background()), ErrOperatorWorkerConfig)
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
