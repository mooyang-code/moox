package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/ashare"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// stockFlattenFixture 把 account-a 改成 A 股模拟账户，持有 600519.XSHG（total 股，其中 available 股可卖）。
func stockFlattenFixture(t *testing.T, total, available string) *operatorFixture {
	t.Helper()
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	db := fixture.store.DBForTest()
	require.NoError(t, db.Exec("UPDATE t_trading_accounts SET c_exchange='ASHARE', c_markets='STOCK', c_settlement_asset='CNY' WHERE c_trading_account_id='account-a'").Error)
	require.NoError(t, db.Exec("UPDATE t_logical_accounts SET c_settlement_asset='CNY'").Error)
	require.NoError(t, db.Exec("DELETE FROM t_logical_account_members WHERE c_trading_account_id='account-b'").Error)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			BaseAsset: "600519.XSHG", QuoteAsset: "CNY", SettlementAsset: "CNY",
			ExchangeQuantityStep: "100", MinExchangeQuantity: "100", PriceTick: "0.01",
			MaxBuyPriceRatio: "1.02", MinSellPriceRatio: "0.98", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "bought", ClientOrderID: "bought",
			Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG", InstrumentID: "600519.XSHG",
			OrderType: "MARKET", Side: "BUY", Quantity: total, FilledQuantity: total, ReferencePrice: "10", State: "FILLED",
			OwnerType: "OPERATOR", OwnerID: "action-0", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
	account, err := fixture.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Wallet: "UNIFIED", Asset: "CNY", Available: "100000", Total: "100000"},
		{Wallet: "UNIFIED", Asset: "600519.XSHG", Available: available, Total: total, Locked: "0"},
	}
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: account.SnapshotSourceTime, LastSyncAt: account.LastSyncAt,
		})
	}))
	fixture.prices.quote = Quote{
		Price: shared.MustDecimal("10"), UpdatedAt: fixture.now, Bid: shared.MustDecimal("9.99"), Ask: shared.MustDecimal("10"),
		LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9.8"),
	}
	return fixture
}

func flattenAccountA(t *testing.T, fixture *operatorFixture) FlattenAccountResult {
	t.Helper()
	result, err := fixture.service().FlattenLogicalAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-stock", LogicalAccountID: "logical-1", Reason: "清仓",
	})
	require.NoError(t, err)
	var accounts struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}
	require.NotNil(t, result.Action.ResultJSON)
	require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &accounts), *result.Action.ResultJSON)
	for _, account := range accounts.Accounts {
		if account.TradingAccountID == "account-a" {
			return account
		}
	}
	t.Fatalf("结果里没有 account-a：%s", *result.Action.ResultJSON)
	return FlattenAccountResult{}
}

// A 股清仓：零股一次卖完；发限价 IOC，价格先截到价格笼子（基准价是买一），再截到跌停价之上。
func TestFlattenStockSellsOddLotWithLimitIOC(t *testing.T) {
	fixture := stockFlattenFixture(t, "150", "150")
	flattenAccountA(t, fixture)
	require.Len(t, fixture.orders.specs, 1)
	spec := fixture.orders.specs[0]
	require.Equal(t, exchange.SideSell, spec.Side)
	require.Equal(t, "150", spec.Quantity.String())
	require.Equal(t, exchange.OrderTypeLimit, spec.Type)
	require.Equal(t, exchange.FillPolicyIOC, spec.FillPolicy)
	// 3% 滑点到 9.7，价格笼子（买一 9.99 × 0.98 = 9.7902，10 个价位 9.89，取更宽）→ 9.80，再截到跌停 9.8。
	require.Equal(t, "9.8", spec.LimitPrice.String())
}

// 只因 T+1 卖不出：不下单，原因写“当日买入，次日可卖”，按终结处理，不无限重试。
func TestFlattenStockT1IsFinal(t *testing.T) {
	fixture := stockFlattenFixture(t, "300", "0")
	account := flattenAccountA(t, fixture)
	require.Empty(t, fixture.orders.specs)
	require.Len(t, account.Remaining, 1)
	require.Equal(t, "当日买入，次日可卖", account.Remaining[0].Reason)
}

type stockSessionCatalog map[exchange.Exchange]execution.ExchangeDescriptor

func (c stockSessionCatalog) Descriptor(name exchange.Exchange) (execution.ExchangeDescriptor, bool) {
	descriptor, ok := c[name]
	return descriptor, ok
}

// A 股手动下单不在交易时段时直接拒绝，提示写明时段；不建动作、不下单。
func TestManualOrderRejectedOutsideAShareSession(t *testing.T) {
	fixture := stockFlattenFixture(t, "100", "100")
	session, err := ashare.NewSession()
	require.NoError(t, err)
	service := fixture.service()
	service.Exchanges = stockSessionCatalog{exchange.ExchangeAShare: {
		Exchange: exchange.ExchangeAShare, Session: session, SessionHours: "A 股 09:30-11:30、13:00-14:57",
	}}
	service.Now = func() time.Time { return time.Date(2026, 11, 6, 4, 0, 0, 0, time.UTC) } // 12:00 午休
	_, err = service.PlaceManualOrder(context.Background(), ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-stock", TradingAccountID: "account-a", ClientOrderID: "manual-stock-client",
		InstrumentID: "600519.XSHG", Type: exchange.OrderTypeMarket, Side: exchange.SideSell, Quantity: shared.MustDecimal("100"), Reason: "测试",
	})
	require.ErrorIs(t, err, ErrInvalidCommand)
	require.ErrorContains(t, err, "不在交易时段（A 股 09:30-11:30、13:00-14:57）")
	require.Empty(t, fixture.orders.specs)
}

func flattenStockAction(t *testing.T, fixture *operatorFixture) (FlattenAccountResult, string) {
	t.Helper()
	result, err := fixture.service().FlattenLogicalAccount(context.Background(), FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-stock", LogicalAccountID: "logical-1", Reason: "清仓",
	})
	require.NoError(t, err)
	var accounts struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &accounts))
	for _, account := range accounts.Accounts {
		if account.TradingAccountID == "account-a" {
			return account, result.Action.Status
		}
	}
	t.Fatal("结果里没有 account-a")
	return FlattenAccountResult{}, ""
}

// 停牌、跌停（卖出时没有买一）：不下单，写中文原因，按可重试处理（动作保持运行，由清仓动作的 30 分钟期限兜底）。
func TestFlattenStockHaltAndLimitDownAreRetryable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		quote  Quote
		reason string
	}{
		{"停牌", Quote{Price: shared.MustDecimal("10"), Halted: true}, "停牌，复牌后再卖"},
		{"跌停", Quote{Price: shared.MustDecimal("9.8"), LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9.8"), Ask: shared.MustDecimal("9.8")}, "跌停，没有买一，打开后再卖"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := stockFlattenFixture(t, "100", "100")
			tc.quote.UpdatedAt = fixture.now
			fixture.prices.quote = tc.quote
			account, status := flattenStockAction(t, fixture)
			require.Empty(t, fixture.orders.specs)
			require.Equal(t, "RUNNING", status, "可重试，不终结")
			require.Len(t, account.Remaining, 1)
			require.Equal(t, tc.reason, account.Remaining[0].Reason)
		})
	}
}

// 一笔 IOC 零成交结束后不终结：子单号带上尝试次数，下一笔换号再下；休市时被撤掉的单不算零成交次数。
func TestFlattenStockRetriesAfterUnfilledIOC(t *testing.T) {
	fixture := stockFlattenFixture(t, "100", "100")
	limit := "9.7"
	for index, reason := range []string{"", paper.OutOfSessionReason} {
		fixture.order(t, store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: fmt.Sprintf("old-%d", index), ClientOrderID: fmt.Sprintf("old-%d", index),
			ExchangeOrderID: fmt.Sprintf("old-%d", index), Exchange: "ASHARE", MarketType: "STOCK", ExchangeSymbol: "600519.XSHG",
			InstrumentID: "600519.XSHG", OrderType: "LIMIT", TimeInForce: "IOC", LimitPrice: &limit, Side: "SELL", Quantity: "100",
			FilledQuantity: "0", ReferencePrice: "10", State: "CANCELED", RejectReason: reason,
			OwnerType: "OPERATOR", OwnerID: "flatten-stock", LogicalAccountID: "logical-1", Version: 1,
		})
	}
	_, status := flattenStockAction(t, fixture)
	require.NotEqual(t, "PARTIAL", status)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, flattenClientOrderIDForSpec("flatten-stock", "account-a",
		exchange.NewSymbolKey(exchange.MarketTypeStock, "600519.XSHG"), exchange.SideSell, shared.MustDecimal("100"), 2),
		fixture.orders.specs[0].ClientOrderID)

	history := delistHistory{unfilled: flattenMaxUnfilled}
	require.True(t, isFlattenFinal(checkStockFlattenAttempt(history, exchange.SideSell, Quote{Price: shared.MustDecimal("10")})))
}
