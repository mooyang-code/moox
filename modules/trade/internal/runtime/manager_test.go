package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type managerAccountSource struct {
	mu       sync.Mutex
	accounts []store.ExchangeAccountRecord
}

func (s *managerAccountSource) ListEnabledExchangeAccounts(
	context.Context,
) ([]store.ExchangeAccountRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.ExchangeAccountRecord(nil), s.accounts...), nil
}

func (s *managerAccountSource) set(accounts []store.ExchangeAccountRecord) {
	s.mu.Lock()
	s.accounts = append([]store.ExchangeAccountRecord(nil), accounts...)
	s.mu.Unlock()
}

type managedSessionStub struct {
	ready   atomic.Bool
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

type delayedStopSession struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

type reconnectingSessionStub struct {
	ready         atomic.Bool
	runCalls      atomic.Int32
	secondStarted chan struct{}
	stopped       chan struct{}
	startOnce     sync.Once
	stopOnce      sync.Once
}

func (s *reconnectingSessionStub) Run(ctx context.Context) error {
	if s.runCalls.Add(1) == 1 {
		return errors.New("private stream disconnected")
	}
	s.startOnce.Do(func() { close(s.secondStarted) })
	s.ready.Store(true)
	<-ctx.Done()
	s.ready.Store(false)
	s.stopOnce.Do(func() { close(s.stopped) })
	return ctx.Err()
}

func (s *reconnectingSessionStub) Ready() bool { return s.ready.Load() }

type failingSessionStub struct {
	runCalls atomic.Int32
	started  chan struct{}
	once     sync.Once
}

func (s *failingSessionStub) Run(context.Context) error {
	s.runCalls.Add(1)
	s.once.Do(func() { close(s.started) })
	return errors.New("private stream disconnected")
}

func (*failingSessionStub) Ready() bool { return false }

func (s *delayedStopSession) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	<-s.release
	return ctx.Err()
}

func (*delayedStopSession) Ready() bool { return false }

func (s *managedSessionStub) Run(ctx context.Context) error {
	s.once.Do(func() { close(s.started) })
	s.ready.Store(true)
	<-ctx.Done()
	s.ready.Store(false)
	close(s.stopped)
	return ctx.Err()
}

func (s *managedSessionStub) Ready() bool { return s.ready.Load() }

func TestManagerOwnsExactlyOneSessionPerEnabledAccount(t *testing.T) {
	account := store.ExchangeAccountRecord{ExchangeAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{account}}
	session := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	var factoryCalls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			factoryCalls.Add(1)
			return session, nil
		},
		PollInterval: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-session.started

	require.Eventually(t, func() bool {
		return manager.Ready("account-1")
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), factoryCalls.Load())
	require.Equal(t, SessionSnapshot{Enabled: 1, Ready: 1}, manager.Snapshot())

	time.Sleep(30 * time.Millisecond)
	require.Equal(t, int32(1), factoryCalls.Load(), "polling must not duplicate sessions")
	source.set(nil)
	require.Eventually(t, func() bool {
		return manager.Snapshot().Enabled == 0
	}, time.Second, 10*time.Millisecond)
	<-session.stopped

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestManagerReadyForRejectsStaleSessionConfiguration(t *testing.T) {
	record := store.ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1",
		Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "LIVE",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: "ENABLED", SyncSymbols: []string{"BTCUSDT"},
		LeverageSettings: store.LeverageSettings{},
	}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{record}}
	session := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			return session, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, manager.reconcile(ctx))
	<-session.started
	account := exchangeaccount.Account{
		ID: "account-1", SpaceID: "space-1",
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot,
		ExecutionMode:      exchange.ExecutionModeLive,
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: exchange.AccountStatusEnabled, SyncSymbols: []string{"BTCUSDT"},
		LeverageSettings: map[string]shared.Decimal{},
	}
	require.True(t, manager.ReadyFor(account))
	account.CredentialSecretID = "secret-2"
	require.False(t, manager.ReadyFor(account))
	account.CredentialSecretID = "secret-1"
	manager.Invalidate(account.ID)
	require.False(t, manager.ReadyFor(account))
	manager.stopAll()
}

func TestManagerReplacesInvalidatedSessionWithUnchangedConfiguration(t *testing.T) {
	record := store.ExchangeAccountRecord{ExchangeAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{record}}
	first := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	second := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	var calls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			if calls.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, manager.reconcile(ctx))
	<-first.started
	manager.Invalidate(record.ExchangeAccountID)
	<-first.stopped
	require.NoError(t, manager.reconcile(ctx))
	<-second.started
	require.Eventually(t, func() bool {
		return manager.Ready(record.ExchangeAccountID)
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int32(2), calls.Load())
	manager.stopAll()
}

func TestManagerWaitsForRemovedSessionBeforeReplacement(t *testing.T) {
	account := store.ExchangeAccountRecord{ExchangeAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{account}}
	first := &delayedStopSession{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	second := &managedSessionStub{started: make(chan struct{}), stopped: make(chan struct{})}
	var calls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			if calls.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, manager.reconcile(ctx))
	<-first.started

	source.set(nil)
	removed := make(chan error, 1)
	go func() { removed <- manager.reconcile(ctx) }()
	<-first.canceled
	source.set([]store.ExchangeAccountRecord{account})
	require.Equal(t, int32(1), calls.Load())
	require.NotEmpty(t, manager.sessions)

	close(first.release)
	require.NoError(t, <-removed)
	require.NoError(t, manager.reconcile(ctx))
	<-second.started
	require.Equal(t, int32(2), calls.Load())
	manager.stopAll()
	<-second.stopped
}

func TestManagerRunWaitsForSessionShutdown(t *testing.T) {
	account := store.ExchangeAccountRecord{ExchangeAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{account}}
	session := &delayedStopSession{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			return session, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-session.started
	cancel()
	<-session.canceled
	select {
	case <-done:
		t.Fatal("Manager.Run returned before the session stopped")
	default:
	}
	close(session.release)
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestManagerReconnectsTheExistingSessionAfterRunFailure(t *testing.T) {
	account := store.ExchangeAccountRecord{ExchangeAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.ExchangeAccountRecord{account}}
	session := &reconnectingSessionStub{
		secondStarted: make(chan struct{}),
		stopped:       make(chan struct{}),
	}
	var factoryCalls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.ExchangeAccountRecord) (ManagedSession, error) {
			factoryCalls.Add(1)
			return session, nil
		},
		PollInterval: 5 * time.Millisecond,
		RetryMin:     time.Millisecond,
		RetryMax:     2 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	select {
	case <-session.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("session was not restarted after its first Run failure")
	}
	require.Eventually(t, func() bool {
		return manager.Ready("account-1")
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, int32(2), session.runCalls.Load())
	require.Equal(t, int32(1), factoryCalls.Load(), "reconnect must reuse the session")

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	<-session.stopped
}

func TestManagerCancellationInterruptsReconnectBackoff(t *testing.T) {
	session := &failingSessionStub{started: make(chan struct{})}
	manager := &Manager{RetryMin: time.Hour, RetryMax: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.runSession(ctx, session)
		close(done)
	}()
	<-session.started

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session reconnect backoff ignored cancellation")
	}
	require.Equal(t, int32(1), session.runCalls.Load())
}
