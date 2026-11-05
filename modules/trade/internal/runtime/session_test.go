package runtime

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/adaptertest"
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
	adaptertest.Stub
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

func (*scriptedSessionAdapter) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
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
	exchange.SymbolKey,
	string,
) ([]exchange.Fill, string, error) {
	a.record("ListRecentFills")
	return nil, "9", nil
}
func (*scriptedSessionAdapter) GetOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}
func (*scriptedSessionAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*scriptedSessionAdapter) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (a *scriptedSessionAdapter) SetLeverage(
	context.Context,
	exchange.SymbolKey,
	shared.Decimal,
) error {
	a.record("SetLeverage")
	return nil
}
func (a *scriptedSessionAdapter) SetMarginMode(
	ctx context.Context,
	_ exchange.SymbolKey,
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
	if err := handler.OnPosition(ctx, exchange.Position{Market: exchange.MarketTypeSwap,
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
		// 生产环境里同步服务的会话状态由 Manager 提供；激活前缓冲的持仓事件会让持久化状态等待排队的完整同步确认。
		SessionState: readySessionState(true),
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
		return len(adapter.callSnapshot()) >= 7
	}, 2*time.Second, 10*time.Millisecond)
	// 缓冲的持仓事件只关闭准入，防抖后触发轻量同步：只读持仓和账户，不读全部挂单。
	require.Equal(t, []string{
		"LoadInstruments",
		"Subscribe",
		"GetAccountSnapshot",
		"ListPositionSnapshots",
		"ListOpenOrders",
		"ListPositionSnapshots",
		"GetAccountSnapshot",
	}, adapter.callSnapshot())
	// 激活前缓冲的持仓事件在 READY 交接时才应用，应用期间持久化状态会短暂回到未就绪、等完整同步确认后再就绪：
	// session.Ready() 为真时库里的就绪位可能还没落定，要等它稳定为真。
	require.Eventually(t, func() bool {
		stored, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
		return err == nil && stored.Ready
	}, 2*time.Second, 10*time.Millisecond)
	var positionRows int64
	require.NoError(t, tradeStore.DBForTest().Raw(`
		SELECT COUNT(*) FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?
	`, "space-1", "account-1", "BTC-USDT").Scan(&positionRows).Error)
	require.Zero(t, positionRows, "持仓事件不写持仓，数量只由成交投影和 REST 决定")

	disconnectErr := errors.New("private stream disconnected")
	adapter.disconnect <- disconnectErr
	require.ErrorIs(t, <-done, disconnectErr)
	require.False(t, session.Ready())
	stored, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
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
	require.NoError(t, handler.OnPosition(ctx, exchange.Position{Market: exchange.MarketTypeSwap,
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
	require.False(t, stored.Ready, "激活前缓冲的持仓事件已使持久化状态进入等待完整同步，激活不能把它写回就绪")
	session.Sync.SessionState = readySessionState(true)
	_, err = session.Sync.SyncAccount(ctx, account.TradingAccountID)
	require.NoError(t, err)
	stored, err = tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.True(t, stored.Ready, "排队的完整同步确认后恢复就绪")
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
	require.NoError(t, handler.OnPosition(ctx, exchange.Position{Market: exchange.MarketTypeSwap,
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
		Exchange: "BINANCE", Markets: []string{"SWAP"}, ExecutionMode: "LIVE",
		Environment:        "TESTNET",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		MarginMode: "CROSS", Status: "ENABLED",
		LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "5"},
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(account)
	}))
	return account
}

type readySessionState bool

func (s readySessionState) Ready(string) bool { return bool(s) }

// 激活前只缓冲了私有余额事件（没有成交或持仓）：余额事件只合并了部分字段，激活同样不能把持久化就绪写回 true。
func TestSessionHandlerKeepsAdmissionClosedAfterBufferedAccountEvent(t *testing.T) {
	ctx := context.Background()
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	session := &ExchangeSession{
		Account: account,
		Sync: &accountsync.Service{
			Store: tradeStore, Adapters: sessionAdapterSource{adapter: &scriptedSessionAdapter{}},
			Fills: &consumer.Reducer{Store: tradeStore},
		},
	}
	handler := newSessionHandler(session.applyEvent)
	require.NoError(t, handler.activate(ctx, func() error {
		return session.Sync.SetReady(ctx, account.TradingAccountID, true, nil)
	}))
	require.NoError(t, handler.OnAccountSnapshot(ctx, exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("100"), ExchangeUpdatedAt: time.UnixMilli(2_500),
		Present: exchange.AccountSnapshotPresence{AvailableFunds: true},
	}))
	require.NoError(t, handler.finishActivation(ctx))
	stored, err := tradeStore.GetTradingAccountByID(ctx, account.TradingAccountID)
	require.NoError(t, err)
	require.False(t, stored.Ready, "缓冲的私有余额事件在完整同步前不得恢复准入")
	require.Equal(t, int64(2_500), stored.BalancePendingUntil)
}

// pollingSessionAdapter 只暴露 ExecutionAdapter，隐藏底层的私有流。
type pollingSessionAdapter struct {
	execution.ExecutionAdapter
}

func TestExchangeSessionWithoutPrivateStreamBecomesReadyByPolling(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	account := seedRuntimeAccount(t, tradeStore)
	scripted := &scriptedSessionAdapter{disconnect: make(chan error, 1), positionBuffered: make(chan struct{})}
	adapter := pollingSessionAdapter{ExecutionAdapter: scripted}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore}, SessionState: readySessionState(true),
	}
	session := &ExchangeSession{Account: account, Adapter: adapter, Sync: syncService, SyncInterval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = session.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	require.Eventually(t, session.Ready, 2*time.Second, 10*time.Millisecond)
	require.NotContains(t, scripted.callSnapshot(), "Subscribe")
}

func TestSessionSetsSymbolConfigOnceUntilForgotten(t *testing.T) {
	account := store.TradingAccountRecord{ExecutionMode: string(exchange.ExecutionModeLive)}
	scripted := &scriptedSessionAdapter{disconnect: make(chan error, 1)}
	session := &ExchangeSession{Account: account, Adapter: scripted}
	leverage := shared.MustDecimal("3")
	ctx := context.Background()

	require.NoError(t, session.EnsureSymbolConfig(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), leverage))
	require.NoError(t, session.EnsureSymbolConfig(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), leverage))
	require.Equal(t, []string{"SetMarginMode", "SetLeverage"}, scripted.callSnapshot())

	session.ForgetSymbolConfig(exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"))
	require.NoError(t, session.EnsureSymbolConfig(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), leverage))
	require.Len(t, scripted.callSnapshot(), 4)

	session.ForgetSymbolConfig(exchange.NewSymbolKey(exchange.MarketTypeSwap, store.DefaultLeverageKey))
	require.NoError(t, session.EnsureSymbolConfig(ctx, exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), leverage))
	require.Len(t, scripted.callSnapshot(), 6)
}

func TestPaperSessionSkipsSymbolConfig(t *testing.T) {
	scripted := &scriptedSessionAdapter{disconnect: make(chan error, 1)}
	session := &ExchangeSession{
		Account: store.TradingAccountRecord{ExecutionMode: string(exchange.ExecutionModePaper)}, Adapter: scripted,
	}
	require.NoError(t, session.EnsureSymbolConfig(context.Background(), exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT"), shared.MustDecimal("3")))
	require.Empty(t, scripted.callSnapshot())
}
