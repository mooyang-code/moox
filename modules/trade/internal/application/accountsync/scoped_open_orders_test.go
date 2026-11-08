package accountsync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// scopedAdapter 记录挂单查询的方式：全量（不传 symbol）还是按标的。
type scopedAdapter struct {
	*syncAdapter
	fullCalls int
	scoped    [][]exchange.SymbolKey
}

func (a *scopedAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	a.fullCalls++
	return nil, nil
}

func (a *scopedAdapter) ListOpenOrdersForSymbols(_ context.Context, symbols []exchange.SymbolKey) ([]exchange.Order, error) {
	a.scoped = append(a.scoped, append([]exchange.SymbolKey(nil), symbols...))
	return nil, nil
}

func seedSpotAccountWithTargetAndHoldings(t *testing.T, tradeStore *store.Store) store.TradingAccountRecord {
	t.Helper()
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, base := range []string{"ETH", "BNB", "DOGE"} {
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SPOT", ExchangeSymbol: base + "USDT",
				InstrumentID: base + "-USDT-SPOT", BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
		}
		_, _, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1", InstanceID: "runner-1", SessionID: "session-1",
			StrategyID: "strategy-1", BarEndTime: 1, EffectiveAt: 1, ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
			Targets: []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "1"}}, Status: "PENDING", AcceptedAt: 1,
		})
		return err
	}))
	require.NoError(t, tradeStore.DBForTest().Exec(`UPDATE t_trading_accounts SET c_markets = 'SPOT' WHERE c_trading_account_id = 'account-1'`).Error)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Wallet: "UNIFIED", Asset: "USDT", Available: "100", Total: "100"},
		{Wallet: "UNIFIED", Asset: "BNB", Available: "2", Total: "2"},
		{Wallet: "UNIFIED", Asset: "DOGE", Available: "0", Total: "0"},
	}
	return account
}

// 周期对账（统一账户现货）只查当前目标、持有资产和本地未结订单涉及的现货标的；
// 会话启动、增量同步和手动全量同步仍读全部挂单。没有按标的查询能力的适配器照常读全部挂单。
func TestReconcileReadsOpenOrdersPerSymbolWhileOtherModesReadAll(t *testing.T) {
	tradeStore := openSyncStore(t)
	account := seedSpotAccountWithTargetAndHoldings(t, tradeStore)
	service := &Service{Store: tradeStore, Now: func() time.Time { return time.UnixMilli(10_000) }}
	adapter := &scopedAdapter{syncAdapter: &syncAdapter{}}

	_, err := service.listOpenOrders(context.Background(), adapter, account, syncReconcile)
	require.NoError(t, err)
	require.Zero(t, adapter.fullCalls, "周期对账不能不带 symbol 读全部挂单")
	require.Len(t, adapter.scoped, 1)
	require.Equal(t, []exchange.SymbolKey{
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBUSDT"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "ETHUSDT"),
	}, adapter.scoped[0], "持有的 BNB 和目标里的 ETH；没有持有、不在目标里的 DOGE 不查")

	// 本地有一笔未结束的现货订单（老订单，早于迟到成交窗口），标的既没持有、也不在目标里：必须被查到，
	// 否则它在交易所的真实状态（挂着、成交、撤销）对账不到。
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "old-resting", TradingAccountID: "account-1", ClientOrderID: "old-resting-client",
			ExchangeOrderID: "77", MarketType: "SPOT", InstrumentID: "DOGE-USDT-SPOT", ExchangeSymbol: "DOGEUSDT",
			OrderType: "MARKET", Side: "BUY", Quantity: "10", ReferencePrice: "0.1", ReferencePriceAt: 1,
			OwnerType: "EXTERNAL", OwnerID: "external-77", LogicalAccountID: "logical-1", State: "OPEN", FilledQuantity: "0",
			Version: 1, SubmittedAt: 1,
		})
	}))
	_, err = service.listOpenOrders(context.Background(), adapter, account, syncReconcile)
	require.NoError(t, err)
	require.Len(t, adapter.scoped, 2)
	require.Equal(t, []exchange.SymbolKey{
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "BNBUSDT"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "DOGEUSDT"),
		exchange.NewSymbolKey(exchange.MarketTypeSpot, "ETHUSDT"),
	}, adapter.scoped[1], "未结束的本地订单不受迟到成交窗口限制，它的标的一定会查")

	for _, mode := range []syncMode{syncIncremental, syncFull} {
		_, err = service.listOpenOrders(context.Background(), adapter, account, mode)
		require.NoError(t, err)
	}
	require.Equal(t, 2, adapter.fullCalls, "增量同步、手动全量同步读全部挂单")
	require.Len(t, adapter.scoped, 2)

	plain := &syncAdapter{order: exchange.Order{ExchangeSymbol: "BTC-USDT"}}
	orders, err := service.listOpenOrders(context.Background(), plain, account, syncReconcile)
	require.NoError(t, err)
	require.Len(t, orders, 1, "没有按标的查询能力的适配器照常读全部挂单")
}

// 要查的现货标的超过上限时退回全量查：逐个串行查的延迟随标的数增长。
func TestReconcileFallsBackToFullQueryWhenTooManySymbols(t *testing.T) {
	tradeStore := openSyncStore(t)
	account := seedSpotAccountWithTargetAndHoldings(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for index := 0; index < scopedOpenOrderLimit; index++ {
			base := fmt.Sprintf("C%02d", index)
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SPOT", ExchangeSymbol: base + "USDT",
				InstrumentID: base + "-USDT-SPOT", BaseAsset: base, QuoteAsset: "USDT", SettlementAsset: "USDT",
				ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}); err != nil {
				return err
			}
			account.Snapshot.Balances = append(account.Snapshot.Balances, store.AssetBalance{Wallet: "UNIFIED", Asset: base, Available: "1", Total: "1"})
		}
		return nil
	}))
	service := &Service{Store: tradeStore, Now: func() time.Time { return time.UnixMilli(10_000) }}
	adapter := &scopedAdapter{syncAdapter: &syncAdapter{}}

	_, err := service.listOpenOrders(context.Background(), adapter, account, syncReconcile)
	require.NoError(t, err)
	require.Equal(t, 1, adapter.fullCalls, "标的超过上限：退回全量查")
	require.Empty(t, adapter.scoped)
}
