package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type matcherScanFunc func(context.Context) error

func (f matcherScanFunc) Scan(ctx context.Context) error { return f(ctx) }

func TestPaperMatcherWorkerReportsSharedFailureRecoveryAndExit(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	w := NewPaperMatcherWorker(matcherScanFunc(func(context.Context) error {
		if fail.Load() {
			return errors.New("database unavailable")
		}
		return nil
	}), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	w.Wake()
	require.Eventually(t, func() bool { ready, lastError := w.State(); return !ready && lastError == "database unavailable" }, time.Second, time.Millisecond)
	fail.Store(false)
	w.Wake()
	require.Eventually(t, func() bool { ready, lastError := w.State(); return ready && lastError == "" }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.False(t, w.Ready())
}

func TestPaperMatcherWorkerNeverBecomesReadyAfterCanceledScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := NewPaperMatcherWorker(matcherScanFunc(func(context.Context) error { cancel(); return nil }), time.Second)
	w.scan(ctx)
	require.False(t, w.Ready())
}

func TestPaperSessionReadyImmediatelyChecksOwnFaultAndPublicWorker(t *testing.T) {
	workerReady := true
	accountState := paper.MatcherState{Ready: true, Generation: 1}
	s := &ExchangeSession{Account: store.TradingAccountRecord{TradingAccountID: "a", ExecutionMode: "PAPER"}, PaperMatcherReady: func() bool { return workerReady }, PaperAccountState: func(id string) paper.MatcherState { require.Equal(t, "a", id); return accountState }}
	s.ready.Store(true)
	require.True(t, s.Ready())
	accountState.Ready = false
	require.False(t, s.Ready(), "ReadyFor must fail immediately, not wait for the session poll")
	accountState.Ready = true
	workerReady = false
	require.False(t, s.Ready())
	workerReady = true
	require.True(t, s.Ready())
	s.ready.Store(false)
	require.False(t, s.Ready(), "healthy matcher does not bypass session initialization")
}

func TestPaperSessionVerifiedSyncRecoveryUsesObservedGeneration(t *testing.T) {
	state := paper.MatcherState{Ready: false, Generation: 7}
	var recovered uint64
	s := &ExchangeSession{Account: store.TradingAccountRecord{TradingAccountID: "a"}, PaperAccountState: func(string) paper.MatcherState { return state }, PaperAccountRecovered: func(id string, generation uint64) { require.Equal(t, "a", id); recovered = generation }}
	generation := s.paperAccountGeneration()
	state.Generation++
	s.recoverPaperAccount(generation)
	require.Equal(t, uint64(7), recovered)
}

func TestPaperSessionSyncDoesNotClearUnverifiedOrderQuoteFault(t *testing.T) {
	state := paper.MatcherState{Ready: false, Generation: 1, Stage: "decision"}
	recovered := false
	s := &ExchangeSession{PaperAccountState: func(string) paper.MatcherState { return state }, PaperAccountRecovered: func(string, uint64) { recovered = true }}
	s.recoverPaperAccountAfterSync(1, []exchange.Order{{}})
	require.False(t, recovered)
	s.recoverPaperAccountAfterSync(1, nil)
	require.True(t, recovered, "no pending candidate remains after successful sync")
	recovered = false
	state.Stage = "refresh"
	s.recoverPaperAccountAfterSync(1, []exchange.Order{{}})
	require.True(t, recovered, "successful snapshot sync repairs a refresh-stage fault")
}
