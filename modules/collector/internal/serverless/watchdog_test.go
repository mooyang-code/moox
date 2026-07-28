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

func TestWatchdogDirectSendBoundariesAndCooldown(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	events, sender := &eventReporterStub{}, &senderStub{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", SpaceID: "crypto", Ready: func() bool { return true },
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
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), sender.count.Load())

	now = now.Add(6 * time.Minute)
	require.NoError(t, handler.Handle(context.Background()))
	require.Equal(t, int32(2), sender.count.Load())
}

func TestWatchdogHealthyMonitorCanaryFailureUsesOnlyEventBus(t *testing.T) {
	events, sender := &eventReporterStub{}, &senderStub{}
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", SpaceID: "crypto", Ready: func() bool { return true },
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
		Enabled: true, ObserverID: "scf-sentinel", SpaceID: "crypto", Ready: func() bool { return true },
		Checks: []WatchdogCheck{func(context.Context) CheckResult { return CheckResult{CheckID: "monitor_ready", Success: true} }},
		Events: events, DirectSender: sender,
	})
	require.NoError(t, err)
	require.Error(t, handler.Handle(context.Background()))
	require.Equal(t, int32(1), sender.count.Load())
}

func TestWatchdogSkipsNotReadyAndOverlap(t *testing.T) {
	var skippedMu sync.Mutex
	var skipped []string
	handler, err := NewWatchdogHandler(WatchdogOptions{
		Enabled: true, ObserverID: "scf-sentinel", Ready: func() bool { return false },
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
