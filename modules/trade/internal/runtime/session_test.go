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
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type sessionAdapterSource struct {
	adapter exchange.Adapter
}

func (s sessionAdapterSource) Adapter(string) (exchange.Adapter, error) {
	return s.adapter, nil
}

type scriptedSessionAdapter struct {
	mu          sync.Mutex
	calls       []string
	disconnect  chan error
	loadStarted chan struct{}
	loadRelease chan struct{}
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
		Symbol: "BTC-USDT", InstrumentID: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		Linear: true, ContractValue: shared.MustDecimal("0.1"),
		ContractValueAsset: "BTC", ExchangeQuantityStep: shared.MustDecimal("1"),
		MinExchangeQuantity: shared.MustDecimal("1"),
		PriceTick:           shared.MustDecimal("0.1"), Status: "TRADING",
		ExchangeUpdatedAt: time.UnixMilli(1_000),
	}}, nil
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
	stored, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
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
	string,
	string,
) ([]exchange.Fill, string, error) {
	a.record("ListRecentFills")
	return nil, "9", nil
}
func (*scriptedSessionAdapter) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}
func (*scriptedSessionAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*scriptedSessionAdapter) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (a *scriptedSessionAdapter) SetLeverage(
	context.Context,
	string,
	shared.Decimal,
) error {
	a.record("SetLeverage")
	return nil
}
func (a *scriptedSessionAdapter) SetMarginMode(
	context.Context,
	string,
	exchange.MarginMode,
) error {
	a.record("SetMarginMode")
	return nil
}
func (a *scriptedSessionAdapter) SubscribePrivate(
	ctx context.Context,
	handler exchange.EventHandler,
) error {
	a.record("SubscribePrivate")
	exchange.NotifyPrivateReady(handler)
	if err := handler.OnPosition(ctx, exchange.Position{
		Symbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.25"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_500),
	}); err != nil {
		return err
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
	adapter := &scriptedSessionAdapter{disconnect: make(chan error, 1)}
	syncService := &accountsync.Service{
		Store: tradeStore, Adapters: sessionAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	session := &ExchangeSession{
		Account: account, Adapter: adapter, Sync: syncService,
		SyncInterval: time.Hour,
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background()) }()

	require.Eventually(t, session.Ready, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{
		"SubscribePrivate",
		"LoadInstruments",
		"SetMarginMode",
		"SetLeverage",
		"GetAccountSnapshot",
		"ListPositionSnapshots",
		"ListOpenOrders",
		"ListRecentFills",
	}, adapter.callSnapshot())
	stored, err := tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, stored.Ready)
	var positionUpdatedAt int64
	require.NoError(t, tradeStore.DBForTest().Raw(`
		SELECT c_exchange_updated_at FROM t_exchange_positions
		WHERE c_space_id = ? AND c_exchange_account_id = ? AND c_symbol = ?
	`, "space-1", "account-1", "BTC-USDT").Scan(&positionUpdatedAt).Error)
	require.Equal(t, int64(2_500), positionUpdatedAt,
		"buffered private event must apply after REST snapshot and before READY")

	disconnectErr := errors.New("private stream disconnected")
	adapter.disconnect <- disconnectErr
	require.ErrorIs(t, <-done, disconnectErr)
	require.False(t, session.Ready())
	stored, err = tradeStore.GetExchangeAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, stored.Ready)
	require.Contains(t, stored.LastError, "private stream disconnected")
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
) store.ExchangeAccountRecord {
	t.Helper()
	account := store.ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
		Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE",
		Environment:        "TESTNET",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		MarginMode: "CROSS", Status: "ENABLED",
		LeverageSettings: store.LeverageSettings{"BTC-USDT": "5"},
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateExchangeAccount(account)
	}))
	return account
}
