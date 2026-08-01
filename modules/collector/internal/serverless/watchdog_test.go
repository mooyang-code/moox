package serverless

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/msgbox"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/stretchr/testify/require"
)

type eventReporterStub struct {
	mu      sync.Mutex
	reports []*observabilitypb.HealthCheckReport
	err     error
}

func (s *eventReporterStub) ReportHealth(_ context.Context, report *observabilitypb.HealthCheckReport, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports, report)
	return s.err
}

type senderStub struct{ count atomic.Int32 }

func (s *senderStub) Send(context.Context, msgbox.Message) error {
	s.count.Add(1)
	return nil
}

type blockingEventReporter struct{ calls atomic.Int32 }

func (s *blockingEventReporter) ReportHealth(ctx context.Context, _ *observabilitypb.HealthCheckReport, _ string) error {
	s.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

type contextCheckingSender struct {
	called   atomic.Bool
	canceled atomic.Bool
}

func (s *contextCheckingSender) Send(ctx context.Context, _ msgbox.Message) error {
	s.called.Store(true)
	s.canceled.Store(ctx.Err() != nil)
	return nil
}

func TestWatchdogDirectSendBoundariesAndCooldown(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	events, sender := &eventReporterStub{}, &senderStub{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", SpaceID: "crypto", Ready: func() bool { return true },
		Checks: []WatchdogCheck{
			func(context.Context) CheckResult {
				return CheckResult{CheckID: "monitor_ready", Kind: "http", Success: false, Error: "down"}
			},
			func(context.Context) CheckResult {
				return CheckResult{CheckID: "market_canary", Kind: "storage", Success: false, Error: "stale"}
			},
		},
		Events: events, DirectSender: sender, Now: clock,
	})
	require.NoError(t, err)
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), sender.count.Load())
	require.Len(t, events.reports, 2)
	require.Equal(t, "scf-node-a", events.reports[0].GetNodeId())
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), sender.count.Load())

	now = now.Add(6 * time.Minute)
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, int32(2), sender.count.Load())
}

func TestWatchdogHealthyMonitorCanaryFailureUsesOnlyEventBus(t *testing.T) {
	events, sender := &eventReporterStub{}, &senderStub{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", SpaceID: "crypto", Ready: func() bool { return true },
		Checks: []WatchdogCheck{
			func(context.Context) CheckResult { return CheckResult{CheckID: "monitor_ready", Success: true} },
			func(context.Context) CheckResult {
				return CheckResult{CheckID: "market_canary", Success: false, Error: "stale"}
			},
		},
		Events: events, DirectSender: sender,
	})
	require.NoError(t, err)
	require.NoError(t, handler.Handle(context.Background()))
	require.Zero(t, sender.count.Load())
	require.Len(t, events.reports, 2)
}

func TestWatchdogEventBusFailureDirectSendsOnce(t *testing.T) {
	events, sender := &eventReporterStub{err: errors.New("eventbus down")}, &senderStub{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", SpaceID: "crypto", Ready: func() bool { return true },
		Checks: []WatchdogCheck{func(context.Context) CheckResult { return CheckResult{CheckID: "monitor_ready", Success: true} }},
		Events: events, DirectSender: sender,
	})
	require.NoError(t, err)
	require.Error(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), sender.count.Load())
}

func TestWatchdogEventBusDeadlineStillUsesFreshDirectContext(t *testing.T) {
	events, sender := &blockingEventReporter{}, &contextCheckingSender{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", SpaceID: "crypto",
		Ready: func() bool { return true }, Timeout: 20 * time.Millisecond,
		Checks: []WatchdogCheck{
			func(context.Context) CheckResult { return CheckResult{CheckID: "monitor_ready", Success: true} },
			func(context.Context) CheckResult { return CheckResult{CheckID: "gateway_ready", Success: true} },
		},
		Events: events, DirectSender: sender,
	})
	require.NoError(t, err)
	require.Error(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), events.calls.Load(), "publishing must stop after the first EventBus failure")
	require.True(t, sender.called.Load())
	require.False(t, sender.canceled.Load(), "direct fallback inherited the exhausted watchdog deadline")
}

func TestWatchdogSkipsNotReadyAndOverlap(t *testing.T) {
	var skippedMu sync.Mutex
	var skipped []string
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a", Ready: func() bool { return false },
		OnSkipped: func(reason string) {
			skippedMu.Lock()
			skipped = append(skipped, reason)
			skippedMu.Unlock()
		},
	})
	require.NoError(t, err)
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, []string{"not_ready"}, skipped)
}

func TestWatchdogDefaultTimeoutLeavesDirectFallbackWithinTimerBudget(t *testing.T) {
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", NodeID: "scf-node-a",
	})
	require.NoError(t, err)
	require.Equal(t, 24*time.Second, handler.options.Timeout)
}

func TestSignedHTTPReadyCheckAddsHealthAuthentication(t *testing.T) {
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header = request.Header.Get("X-Moox-Health-Auth")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	result := SignedHTTPReadyCheck("monitor_ready", server.URL+"/readyz", server.Client(), HealthAuth{
		Version: "moox-health-v1", AccessKey: "scf", SecretKey: "secret",
	})(context.Background())
	require.True(t, result.Success, "%+v", result)
	require.Contains(t, header, "moox-health-v1/scf/")
}

func TestConfirmUnreachableCheckRetriesTransientFailure(t *testing.T) {
	var calls int
	check := ConfirmUnreachableCheck(func(context.Context) CheckResult {
		calls++
		if calls == 1 {
			return CheckResult{CheckID: "monitor_ready", Success: false, ErrorCode: "unreachable", Error: "timeout"}
		}
		return CheckResult{CheckID: "monitor_ready", Success: true}
	}, 0)

	result := check(context.Background())
	require.True(t, result.Success)
	require.Equal(t, 2, calls)
}

func TestConfirmUnreachableCheckReturnsConfirmedFailure(t *testing.T) {
	var calls int
	check := ConfirmUnreachableCheck(func(context.Context) CheckResult {
		calls++
		return CheckResult{CheckID: "monitor_ready", Success: false, ErrorCode: "unreachable", Error: "timeout"}
	}, 0)

	result := check(context.Background())
	require.False(t, result.Success)
	require.Equal(t, "unreachable", result.ErrorCode)
	require.Equal(t, 2, calls)
}

func TestConfirmUnreachableCheckDoesNotRetryHTTPFailure(t *testing.T) {
	var calls int
	check := ConfirmUnreachableCheck(func(context.Context) CheckResult {
		calls++
		return CheckResult{CheckID: "monitor_ready", Success: false, ErrorCode: "http_status", Error: "HTTP 503"}
	}, 0)

	result := check(context.Background())
	require.False(t, result.Success)
	require.Equal(t, 1, calls)
}

func TestConfirmUnreachableCheckStopsWhenContextIsCanceled(t *testing.T) {
	var calls int
	check := ConfirmUnreachableCheck(func(context.Context) CheckResult {
		calls++
		return CheckResult{CheckID: "monitor_ready", Success: false, ErrorCode: "unreachable", Error: "timeout"}
	}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := check(ctx)
	require.False(t, result.Success)
	require.Equal(t, 1, calls)
}
