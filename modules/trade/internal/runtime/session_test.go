package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type sessionAdapterSource struct {
	adapter execution.ExecutionAdapter
}

func (s sessionAdapterSource) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
}

type scriptedSessionAdapter struct {
	mu               sync.Mutex
	calls            []string
	disconnect       chan error
	loadStarted      chan struct{}
	loadRelease      chan struct{}
	positionBuffered chan struct{}
}

func (a *scriptedSessionAdapter) record(call string) {
	a.mu.Lock()
	a.calls = append(a.calls, call)
	a.mu.Unlock()
}

func (a *scriptedSessionAdapter) callSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (*scriptedSessionAdapter) Exchange() exchange.Exchange {
	return exchange.ExchangeBinance
}
func (a *scriptedSessionAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	a.record("LoadInstruments")
	if a.loadStarted != nil {
		close(a.loadStarted)
		<-a.loadRelease
	}
	return []exchange.Instrument{{
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSwap,
		ExchangeSymbol: "BTC-USDT", InstrumentID: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		Linear: true, ContractValue: shared.MustDecimal("0.1"),
		ContractValueAsset: "BTC", ExchangeQuantityStep: shared.MustDecimal("1"),
		MinExchangeQuantity: shared.MustDecimal("1"),
		PriceTick:           shared.MustDecimal("0.1"), Status: "TRADING",
		ExchangeUpdatedAt: time.UnixMilli(1_000),
	}}, nil
}

func (*scriptedSessionAdapter) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	return execution.MarketQuote{Last: shared.MustDecimal("100"), SourceTime: time.UnixMilli(2_000)}, nil
}

func TestExchangeSessionNeverBecomesReadyAfterDisconnectDuringStartup(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	adapter := &scriptedSessionAdapter{
		disconnect:  make(chan error, 1),
		loadStarted: make(chan struct{}),
		loadRelease: make(chan struct{}),
	}
	session := &ExchangeSession{
		Account: account,
		Adapter: adapter,
		Sync: &accountsync.Service{
			Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
			Fills: &consumer.Reducer{Store: tradeStore},
		},
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background()) }()
	<-adapter.loadStarted
	disconnectErr := errors.New("startup disconnect")
	adapter.disconnect <- disconnectErr
	require.Never(t, session.Ready, 100*time.Millisecond, 10*time.Millisecond)
	close(adapter.loadRelease)
	require.ErrorIs(t, <-done, disconnectErr)
	require.False(t, session.Ready())
	stored, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, stored.Ready)
}
func (a *scriptedSessionAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	a.record("GetAccountSnapshot")
	return exchange.AccountSnapshot{
		AvailableFunds:    shared.MustDecimal("1000"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}, nil
}
func (a *scriptedSessionAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	a.record("ListPositionSnapshots")
	return nil, nil
}
func (a *scriptedSessionAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	a.record("ListOpenOrders")
	return nil, nil
}
func (a *scriptedSessionAdapter) ListRecentFills(
	context.Context,
	shared.ExchangeSymbol,
	string,
) ([]exchange.Fill, string, error) {
	a.record("ListRecentFills")
	return nil, "9", nil
}
func (*scriptedSessionAdapter) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}
func (*scriptedSessionAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*scriptedSessionAdapter) CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (a *scriptedSessionAdapter) SetLeverage(
	context.Context,
	shared.ExchangeSymbol,
	shared.Decimal,
) error {
	a.record("SetLeverage")
	return nil
}
func (a *scriptedSessionAdapter) SetMarginMode(
	ctx context.Context,
	_ shared.ExchangeSymbol,
	_ exchange.MarginMode,
) error {
	a.record("SetMarginMode")
	if a.positionBuffered != nil {
		select {
		case <-a.positionBuffered:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (a *scriptedSessionAdapter) Subscribe(
	ctx context.Context,
	handler execution.AccountEventHandler,
) error {
	a.record("Subscribe")
	handler.OnSubscribed()
	if err := handler.OnPosition(ctx, exchange.Position{
		ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.25"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_500),
	}); err != nil {
		return err
	}
	if a.positionBuffered != nil {
		close(a.positionBuffered)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-a.disconnect:
		return err
	}
}

func TestExchangeSessionStartsInExactOrderBuffersThenClearsReadyOnDisconnect(
	t *testing.T,
) {
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	adapter := &scriptedSessionAdapter{
		disconnect: make(chan error, 1), positionBuffered: make(chan struct{}),
	}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	session := &ExchangeSession{
		Account: account, Adapter: adapter, Sync: syncService,
		SyncInterval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("session did not stop before store cleanup")
		}
	})
	go func() {
		defer close(stopped)
		done <- session.Run(ctx)
	}()

	require.Eventually(t, session.Ready, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return len(adapter.callSnapshot()) >= 12
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{
		"LoadInstruments",
		"Subscribe",
		"SetMarginMode",
		"SetLeverage",
		"GetAccountSnapshot",
		"ListPositionSnapshots",
		"ListOpenOrders",
		"ListRecentFills",
		"ListOpenOrders",
		"ListPositionSnapshots",
		"GetAccountSnapshot",
		"ListRecentFills",
	}, adapter.callSnapshot())
	stored, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, stored.Ready)
	var positionUpdatedAt int64
	require.NoError(t, tradeStore.DBForTest().Raw(`
		SELECT c_exchange_updated_at FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?
	`, "space-1", "account-1", "BTC-USDT").Scan(&positionUpdatedAt).Error)
	require.Equal(t, int64(2_500), positionUpdatedAt,
		"buffered private event must apply after REST snapshot and before READY")

	disconnectErr := errors.New("private stream disconnected")
	adapter.disconnect <- disconnectErr
	require.ErrorIs(t, <-done, disconnectErr)
	require.False(t, session.Ready())
	stored, err = tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, stored.Ready)
	require.Contains(t, stored.LastError, "private stream disconnected")
}

func TestSessionHandlerActivationDrainsEventsBeforeReadyHandoff(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	applied := make(chan struct{}, 1)
	handler := newSessionHandler(func(context.Context, privateEvent) error {
		applied <- struct{}{}
		return nil
	})
	require.NoError(t, handler.activate(context.Background(), func() error {
		close(started)
		<-release
		return nil
	}))
	require.NoError(t, handler.handle(context.Background(), privateEvent{}))
	activateDone := make(chan error, 1)
	go func() { activateDone <- handler.finishActivation(context.Background()) }()
	<-started
	select {
	case <-applied:
	default:
		t.Fatal("pre-ready event must apply before readiness persistence")
	}
	eventDone := make(chan error, 1)
	go func() { eventDone <- handler.handle(context.Background(), privateEvent{}) }()
	select {
	case <-eventDone:
		t.Fatal("new event must wait for readiness persistence")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-activateDone)
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("activation did not apply the buffered event")
	}
	require.NoError(t, <-eventDone)
}

func TestSessionHandlerPersistsReadyAfterLateBufferedPosition(t *testing.T) {
	ctx := context.Background()
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	adapter := &scriptedSessionAdapter{}
	session := &ExchangeSession{
		Account: account,
		Sync: &accountsync.Service{
			Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
			Fills: &consumer.Reducer{Store: tradeStore},
		},
	}
	handler := newSessionHandler(session.applyEvent)
	require.NoError(t, handler.activate(ctx, func() error {
		return session.Sync.SetReady(ctx, account.TradingAccountID, true, nil)
	}))
	// This event arrives after the initial activation pass but before its final
	// gate closes, exactly the handoff window between Run's two calls.
	require.NoError(t, handler.OnPosition(ctx, exchange.Position{
		ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.25"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_500),
	}))
	require.NoError(t, handler.finishActivation(ctx))
	session.ready.Store(true)
	require.True(t, session.Ready())
	stored, err := tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.True(t, stored.Ready, "late buffered position must precede durable readiness")
	_, err = session.Sync.SyncAccount(ctx, account.TradingAccountID)
	require.NoError(t, err)
	stored, err = tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.True(t, stored.Ready, "queued sync without SessionState must retain readiness")
}

func TestSessionHandlerReadinessFailureReleasesEventGate(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	callbackErr := errors.New("readiness write failed")
	handler := newSessionHandler(func(context.Context, privateEvent) error { return nil })
	require.NoError(t, handler.activate(ctx, func() error {
		close(started)
		<-release
		return callbackErr
	}))
	done := make(chan error, 1)
	go func() { done <- handler.finishActivation(ctx) }()
	<-started
	eventDone := make(chan error, 1)
	go func() { eventDone <- handler.handle(ctx, privateEvent{}) }()
	close(release)
	require.ErrorIs(t, <-done, callbackErr)
	select {
	case err := <-eventDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("readiness failure left event admission blocked")
	}
}

func TestSessionHandlerPostActivationPositionRequiresFullSync(t *testing.T) {
	ctx := context.Background()
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	adapter := &scriptedSessionAdapter{}
	session := &ExchangeSession{Account: account}
	manager := &Manager{sessions: map[string]*managedEntry{
		account.TradingAccountID: {session: session},
	}}
	session.Sync = &accountsync.Service{
		Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore}, SessionState: manager,
	}
	handler := newSessionHandler(session.applyEvent)
	require.NoError(t, handler.activate(ctx, func() error {
		return session.Sync.SetReady(ctx, account.TradingAccountID, true, nil)
	}))
	require.NoError(t, handler.finishActivation(ctx))
	session.ready.Store(true)
	require.NoError(t, handler.OnPosition(ctx, exchange.Position{
		ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.25"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_500),
	}))
	stored, err := tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.False(t, stored.Ready, "post-activation private position requires full sync")
	require.True(t, manager.Ready(account.TradingAccountID))
	_, err = session.Sync.SyncAccount(ctx, account.TradingAccountID)
	require.NoError(t, err)
	stored, err = tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.True(t, stored.Ready, "full sync must recover readiness from the running session")
}

func openRuntimeStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

func seedRuntimeAccount(
	t *testing.T,
	tradeStore *store.Store,
) store.TradingAccountRecord {
	t.Helper()
	account := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Name: "main",
		Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE",
		Environment:        "TESTNET",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		MarginMode: "CROSS", Status: "ENABLED",
		LeverageSettings: store.LeverageSettings{"BTC-USDT": "5"},
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(account)
	}))
	return account
}
