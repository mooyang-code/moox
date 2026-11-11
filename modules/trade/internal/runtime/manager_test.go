package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type managerAccountSource struct {
	mu       sync.Mutex
	accounts []store.TradingAccountRecord
	failures int
	calls    int
}

func (s *managerAccountSource) ListEnabledTradingAccounts(
	context.Context,
) ([]store.TradingAccountRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failures > 0 {
		s.failures--
		return nil, errors.New("temporary account enumeration failure")
	}
	return append([]store.TradingAccountRecord(nil), s.accounts...), nil
}

func (s *managerAccountSource) set(accounts []store.TradingAccountRecord) {
	s.mu.Lock()
	s.accounts = append([]store.TradingAccountRecord(nil), accounts...)
	s.mu.Unlock()
}

func TestManagerRetriesInitialReconcileAndGatesReadiness(t *testing.T) {
	account := store.TradingAccountRecord{TradingAccountID: "account-1"}
	source := &managerAccountSource{
		accounts: []store.TradingAccountRecord{account},
		failures: 1,
	}
	session := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
			return session, nil
		},
		PollInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	require.Eventually(t, func() bool {
		return manager.Snapshot().Reconciled
	}, time.Second, time.Millisecond)
	<-session.started
	require.Eventually(t, func() bool {
		snapshot := manager.Snapshot()
		return snapshot.Reconciled && snapshot.Enabled == 1 && snapshot.Ready == 1
	}, time.Second, time.Millisecond)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.GreaterOrEqual(t, source.calls, 2)
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
	account := store.TradingAccountRecord{TradingAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{account}}
	session := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	var factoryCalls atomic.Int32
	removed := make(chan string, 1)
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
			factoryCalls.Add(1)
			return session, nil
		},
		PollInterval:     10 * time.Millisecond,
		OnSessionRemoved: func(id string) { removed <- id },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-session.started

	require.Eventually(t, func() bool {
		return manager.Ready("account-1")
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), factoryCalls.Load())
	require.Equal(t, SessionSnapshot{
		Enabled: 1, Ready: 1, Reconciled: true,
	}, manager.Snapshot())

	time.Sleep(30 * time.Millisecond)
	require.Equal(t, int32(1), factoryCalls.Load(), "polling must not duplicate sessions")
	source.set(nil)
	require.Eventually(t, func() bool {
		return manager.Snapshot().Enabled == 0
	}, 5*time.Second, 10*time.Millisecond)
	<-session.stopped
	require.Equal(t, "account-1", <-removed)

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// 实盘账户带账户类型和检测指纹：配置没变时就绪，指纹或密钥变了就不就绪。
func TestManagerReadyForRejectsStaleSessionConfiguration(t *testing.T) {
	inspected := func(fingerprint string) string {
		raw, err := exchange.Inspection{CredentialFingerprint: fingerprint}.Encode()
		require.NoError(t, err)
		return raw
	}
	record := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1",
		Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "LIVE",
		AccountMode:        string(exchange.AccountModeBinanceStandard),
		CredentialSecretID: "secret-1", InspectionJSON: inspected("fingerprint-1"),
		SettlementAsset: "USDT", Status: "ENABLED",
		LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
	}
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{record}}
	session := &managedSessionStub{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
			return session, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, manager.reconcile(ctx))
	<-session.started
	account := tradingaccount.Account{
		ID: "account-1", SpaceID: "space-1",
		Exchange: exchange.ExchangeBinance, Markets: []exchange.MarketType{exchange.MarketTypeSpot},
		ExecutionMode: exchange.ExecutionModeLive, AccountMode: exchange.AccountModeBinanceStandard,
		CredentialSecretID: "secret-1", InspectionJSON: inspected("fingerprint-1"),
		SettlementAsset: "USDT", Status: exchange.AccountStatusEnabled,
		// 杠杆不属于会话配置：改了杠杆不重建会话，也不影响就绪。
		LeverageSettings: map[string]shared.Decimal{"*": shared.MustDecimal("10")},
	}
	require.True(t, manager.ReadyFor(account))
	changed := account
	changed.InspectionJSON = inspected("fingerprint-2")
	require.False(t, manager.ReadyFor(changed), "密钥内容变了、重新检测写入新指纹后，旧会话不能再用")
	changed = account
	changed.CredentialSecretID = "secret-2"
	require.False(t, manager.ReadyFor(changed))
	manager.stopAll()
}

func TestManagerWaitsForRemovedSessionBeforeReplacement(t *testing.T) {
	account := store.TradingAccountRecord{TradingAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{account}}
	first := &delayedStopSession{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	second := &managedSessionStub{started: make(chan struct{}), stopped: make(chan struct{})}
	var calls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
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
	source.set([]store.TradingAccountRecord{account})
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
	account := store.TradingAccountRecord{TradingAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{account}}
	session := &delayedStopSession{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
			return session, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	<-session.started
	cancel()
	<-session.canceled
	// Parent cancellation reaches the session and manager independently.
	// The gate must close while session shutdown is still blocked, not before
	// the manager goroutine has had a chance to observe cancellation.
	require.Eventually(t, func() bool { return !manager.Snapshot().Reconciled }, time.Second, time.Millisecond,
		"shutdown must gate readiness before sessions finish stopping")
	select {
	case <-done:
		t.Fatal("Manager.Run returned before the session stopped")
	default:
	}
	close(session.release)
	require.ErrorIs(t, <-done, context.Canceled)
	require.False(t, manager.Snapshot().Reconciled)
}

func TestManagerReconnectsTheExistingSessionAfterRunFailure(t *testing.T) {
	account := store.TradingAccountRecord{TradingAccountID: "account-1"}
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{account}}
	session := &reconnectingSessionStub{
		secondStarted: make(chan struct{}),
		stopped:       make(chan struct{}),
	}
	var factoryCalls atomic.Int32
	manager := &Manager{
		Accounts: source,
		NewSession: func(store.TradingAccountRecord) (ManagedSession, error) {
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
	}, 5*time.Second, 10*time.Millisecond)
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

func TestManagerFactoryFailureIsAccountLocalAndRecovers(t *testing.T) {
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{{TradingAccountID: "good"}, {TradingAccountID: "bad"}}}
	good := &managedSessionStub{started: make(chan struct{}), stopped: make(chan struct{})}
	bad := &managedSessionStub{started: make(chan struct{}), stopped: make(chan struct{})}
	fail := true
	m := &Manager{Accounts: source, NewSession: func(account store.TradingAccountRecord) (ManagedSession, error) {
		if account.TradingAccountID == "good" {
			return good, nil
		}
		if fail {
			return nil, errors.New("account credentials unavailable")
		}
		return bad, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.stopAll()
	require.NoError(t, m.reconcile(ctx))
	<-good.started
	require.Eventually(t, func() bool { return m.Ready("good") }, time.Second, time.Millisecond)
	snapshot := m.Snapshot()
	require.Equal(t, 2, snapshot.Enabled)
	require.Equal(t, 1, snapshot.Ready)
	require.Empty(t, snapshot.ConfigErrors)
	require.Equal(t, map[string]string{"bad": "account credentials unavailable"}, snapshot.AccountErrors)
	snapshot.AccountErrors["bad"] = "mutated caller copy"
	require.Equal(t, "account credentials unavailable", m.Snapshot().AccountErrors["bad"])
	fail = false
	require.NoError(t, m.reconcile(ctx))
	<-bad.started
	require.Eventually(t, func() bool { return m.Snapshot().Ready == 2 }, time.Second, time.Millisecond)
	require.Empty(t, m.Snapshot().AccountErrors)
	m.stopAll()
	require.False(t, m.Snapshot().Reconciled)
}

func TestManagerStoppingSessionDoesNotCountReady(t *testing.T) {
	session := &managedSessionStub{}
	session.ready.Store(true)
	m := &Manager{sessions: map[string]*managedEntry{"stopping": {session: session, stopping: true}}}
	require.Zero(t, m.Snapshot().Ready)
}

func TestManagerEnumerationFailureClearsReconciled(t *testing.T) {
	source := &managerAccountSource{}
	m := &Manager{Accounts: source, NewSession: func(store.TradingAccountRecord) (ManagedSession, error) { panic("no accounts") }}
	require.NoError(t, m.reconcile(context.Background()))
	require.True(t, m.Snapshot().Reconciled)
	source.failures = 1
	require.Error(t, m.reconcile(context.Background()))
	require.False(t, m.Snapshot().Reconciled)
	require.NotEmpty(t, m.Snapshot().ConfigErrors)
	require.NoError(t, m.reconcile(context.Background()))
	require.True(t, m.Snapshot().Reconciled)
	require.Empty(t, m.Snapshot().ConfigErrors)
}

func TestManagerDuplicateIDsRemainGlobalConfigurationFailure(t *testing.T) {
	source := &managerAccountSource{accounts: []store.TradingAccountRecord{{TradingAccountID: "duplicate"}, {TradingAccountID: "duplicate"}}}
	m := &Manager{Accounts: source, NewSession: func(store.TradingAccountRecord) (ManagedSession, error) { return nil, errors.New("factory failure") }}
	require.NoError(t, m.reconcile(context.Background()))
	snapshot := m.Snapshot()
	require.False(t, snapshot.Reconciled)
	require.Equal(t, []string{"duplicate enabled Exchange account ID duplicate"}, snapshot.ConfigErrors)
	require.Equal(t, "factory failure", snapshot.AccountErrors["duplicate"])
}

// scheduledSession 按给定时长依次运行后断开，记录每次开始和结束的时间。
type scheduledSession struct {
	mu       sync.Mutex
	runs     []time.Duration
	starts   []time.Time
	ends     []time.Time
	finished chan struct{}
}

func (s *scheduledSession) Run(ctx context.Context) error {
	s.mu.Lock()
	index := len(s.starts)
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	if index >= len(s.runs) {
		close(s.finished)
		<-ctx.Done()
		return ctx.Err()
	}
	time.Sleep(s.runs[index])
	s.mu.Lock()
	s.ends = append(s.ends, time.Now())
	s.mu.Unlock()
	return errors.New("disconnected")
}

func (*scheduledSession) Ready() bool { return false }

func TestManagerResetsBackoffAfterStableRun(t *testing.T) {
	previous := stableSessionRun
	stableSessionRun = 100 * time.Millisecond
	defer func() { stableSessionRun = previous }()
	// 三次快速失败把退避翻到 80ms，接着一次稳定运行后断开，下次应只等 RetryMin。
	session := &scheduledSession{
		runs:     []time.Duration{0, 0, 0, 150 * time.Millisecond},
		finished: make(chan struct{}),
	}
	manager := &Manager{RetryMin: 20 * time.Millisecond, RetryMax: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.runSession(ctx, session)
	select {
	case <-session.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not reconnect")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	fastGap := session.starts[3].Sub(session.ends[2])
	stableGap := session.starts[4].Sub(session.ends[3])
	require.GreaterOrEqual(t, fastGap, 70*time.Millisecond, "fast failures must keep doubling")
	require.Less(t, stableGap, 60*time.Millisecond, "a stable run must reset the backoff")
}

// 同一个密钥 ID 下改了密钥内容：重新检测写入新的指纹，会话配置随之不同，会话被重建。
func TestSameSessionConfigDiffersWhenCredentialFingerprintChanges(t *testing.T) {
	account := func(apiKey string) store.TradingAccountRecord {
		raw, err := exchange.Inspection{InspectedAt: time.Now().UTC(), CredentialFingerprint: exchange.CredentialFingerprint(apiKey)}.Encode()
		require.NoError(t, err)
		return store.TradingAccountRecord{TradingAccountID: "a", CredentialSecretID: "secret-1", InspectionJSON: raw}
	}
	require.True(t, sameSessionConfig(account("key-1"), account("key-1")))
	require.False(t, sameSessionConfig(account("key-1"), account("key-2")))
}
