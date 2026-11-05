package accountsync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/adaptertest"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// lagAdapter 模拟持仓接口落后：返回的持仓和账户可以随时改，按接口统计调用次数。
type lagAdapter struct {
	adaptertest.Stub
	mu        sync.Mutex
	positions []exchange.Position
	account   exchange.AccountSnapshot
	calls     map[string]int
}

func newLagAdapter(at time.Time) *lagAdapter {
	return &lagAdapter{
		account: exchange.AccountSnapshot{AvailableFunds: shared.MustDecimal("900"), Equity: shared.MustDecimal("1000"), ExchangeUpdatedAt: at},
		calls:   map[string]int{},
	}
}

func (a *lagAdapter) record(name string) {
	a.mu.Lock()
	a.calls[name]++
	a.mu.Unlock()
}
func (*lagAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (a *lagAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	a.record("account")
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.account, nil
}
func (a *lagAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	a.record("positions")
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]exchange.Position(nil), a.positions...), nil
}
func (a *lagAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	a.record("open_orders")
	return nil, nil
}
func (a *lagAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	a.record("fills")
	return nil, "", nil
}
func (a *lagAdapter) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	a.record("order")
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}
func (*lagAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*lagAdapter) CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}

func lightSyncFixture(t *testing.T, now *time.Time) (*Service, *store.Store, *lagAdapter) {
	t.Helper()
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := newLagAdapter(*now)
	service := &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore}, SessionState: readySessionState(true),
		Now: func() time.Time { return *now },
	}
	return service, tradeStore, adapter
}

func projectPosition(t *testing.T, tradeStore *store.Store, quantity string, at time.Time) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", ExchangeSymbol: "BTC-USDT", PositionSide: "NET",
			SignedQuantity: quantity, EntryPrice: "100", Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: at.UnixMilli(),
		})
	}))
}

func storedPosition(t *testing.T, tradeStore *store.Store) (string, bool) {
	t.Helper()
	var quantity string
	var found bool
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		position, ok, err := tx.GetPosition("space-1", "account-1", exchange.SymbolKey{Market: exchange.MarketTypeSwap, Symbol: "BTC-USDT"}, "NET")
		quantity, found = position.SignedQuantity, ok
		return err
	}))
	return quantity, found
}

// 持仓接口落后时，刚成交投影出的持仓在保护期内不能被删，连续多轮同步也一样；过了保护期以 REST 为准。
func TestLightSyncKeepsFreshPositionWhileRESTLags(t *testing.T) {
	now := time.UnixMilli(100_000)
	service, tradeStore, adapter := lightSyncFixture(t, &now)
	projectPosition(t, tradeStore, "0.25", now.Add(-time.Second))
	for range 3 {
		_, err := service.SyncAccountLight(context.Background(), "account-1")
		require.NoError(t, err)
		quantity, found := storedPosition(t, tradeStore)
		require.True(t, found)
		require.Equal(t, "0.25", quantity)
		now = now.Add(time.Second)
	}
	now = now.Add(20 * time.Second)
	adapter.account.ExchangeUpdatedAt = now
	_, err := service.SyncAccountLight(context.Background(), "account-1")
	require.NoError(t, err)
	_, found := storedPosition(t, tradeStore)
	require.False(t, found, "过了保护期仍和 REST 不一致时以 REST 为准")
}

// 成交投影比 REST 持仓的更新时间晚 1 毫秒：保护期内不被更旧的 REST 覆盖，过了保护期以 REST 为准。
func TestLightSyncOverridesStalePositionAfterGrace(t *testing.T) {
	now := time.UnixMilli(100_000)
	service, tradeStore, adapter := lightSyncFixture(t, &now)
	filledAt := now.Add(-time.Second)
	projectPosition(t, tradeStore, "0.5", filledAt)
	adapter.positions = []exchange.Position{{Market: exchange.MarketTypeSwap,
		ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.25"), EntryPrice: shared.MustDecimal("100"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: filledAt.Add(-time.Millisecond),
	}}
	_, err := service.SyncAccountLight(context.Background(), "account-1")
	require.NoError(t, err)
	quantity, _ := storedPosition(t, tradeStore)
	require.Equal(t, "0.5", quantity)
	now = now.Add(20 * time.Second)
	adapter.account.ExchangeUpdatedAt = now
	_, err = service.SyncAccountLight(context.Background(), "account-1")
	require.NoError(t, err)
	quantity, _ = storedPosition(t, tradeStore)
	require.Equal(t, "0.25", quantity)
}

// 轻量同步只读持仓和账户：一致时不读全部挂单，也不拉成交，永续只要 10 权重。
func TestLightSyncReadsOnlyPositionsAndAccount(t *testing.T) {
	now := time.UnixMilli(100_000)
	service, _, adapter := lightSyncFixture(t, &now)
	_, err := service.SyncAccountLight(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"positions": 1, "account": 1}, adapter.calls)
}

// 持仓和 REST 不一致且已过保护期时，轻量同步补拉这个标的的成交。
func TestLightSyncPullsFillsForInconsistentSymbols(t *testing.T) {
	now := time.UnixMilli(100_000)
	service, tradeStore, adapter := lightSyncFixture(t, &now)
	projectPosition(t, tradeStore, "0.25", now.Add(-time.Minute))
	_, err := service.SyncAccountLight(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, adapter.calls["fills"])
	require.Zero(t, adapter.calls["open_orders"])
}

// 现货持仓来自余额：内容比待确认水位旧的 REST 余额不能恢复准入，内容追上后才恢复。
func TestSpotStaleBalanceDoesNotRestoreReady(t *testing.T) {
	now := time.UnixMilli(100_000)
	tradeStore := openSyncStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			CreatedAt: time.UnixMilli(1), SpaceID: "space-1", TradingAccountID: "spot-1", Name: "spot",
			Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "LIVE", Environment: "TESTNET",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		return tx.RaiseBalancePending("space-1", "spot-1", 90_000)
	}))
	adapter := newLagAdapter(time.UnixMilli(80_000))
	service := &Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore}, SessionState: readySessionState(true),
		Now: func() time.Time { return now },
	}
	result, err := service.SyncAccountLight(context.Background(), "spot-1")
	require.NoError(t, err)
	require.True(t, result.BalancePending)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "spot-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	adapter.account.ExchangeUpdatedAt = time.UnixMilli(95_000)
	result, err = service.SyncAccountLight(context.Background(), "spot-1")
	require.NoError(t, err)
	require.False(t, result.BalancePending)
	account, err = tradeStore.GetTradingAccountByID(context.Background(), "spot-1")
	require.NoError(t, err)
	require.True(t, account.Ready)
}

// 轻量同步、对账成功后都唤醒目标收敛：被写入阻塞原因的标的要靠它重算。
func TestEverySyncModeWakesTargets(t *testing.T) {
	now := time.UnixMilli(100_000)
	service, _, _ := lightSyncFixture(t, &now)
	wakes := 0
	service.Wake = func() { wakes++ }
	for _, run := range []func(context.Context, string) (Result, error){
		service.SyncAccountLight, service.SyncAccountReconcile, service.SyncAccount, service.SyncAccountFull,
	} {
		_, err := run(context.Background(), "account-1")
		require.NoError(t, err)
	}
	require.Equal(t, 4, wakes)
}
