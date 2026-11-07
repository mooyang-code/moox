package target

import (
	"context"
	"strings"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

const pseudoSettings = `{"min_notional_swap":"0","min_notional_spot":"0"}`

// iocChild 建一笔本目标已提交、已结束的伪市价子单（account-a，合约）。
func (f *targetFixture) iocChild(t *testing.T, orderID string, quantity, filled string, attempt int) {
	t.Helper()
	f.iocChildOn(t, "account-a", "BINANCE", "BTCUSDT", orderID, quantity, filled, attempt)
}

func (f *targetFixture) iocChildOn(t *testing.T, accountID, exchangeName, symbol, orderID string, quantity, filled string, attempt int) {
	t.Helper()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID, TradingAccountID: accountID, LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: orderID, Exchange: exchangeName, MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: symbol,
		OrderType: "LIMIT", TimeInForce: "IOC", Side: "BUY", Quantity: quantity, LimitPrice: strPtr("101.5"), ReferencePrice: "100",
		FilledQuantity: filled, AveragePrice: "100", OwnerType: "TARGET", OwnerID: "target-current", State: "EXPIRED",
		Attempt: attempt, SubmittedAt: int64(1_000 + attempt), FinishedAt: int64(1_100 + attempt), Version: 1,
	})
}

func TestPseudoMarketBuyIsLimitIOCAtReferencePlusSlippage(t *testing.T) {
	f := swapPolicyFixture(t, pseudoSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.Equal(t, exchange.OrderTypeLimit, spec.Type)
	require.Equal(t, exchange.FillPolicyIOC, spec.FillPolicy)
	require.Equal(t, "101.5", spec.LimitPrice.String(), "100 × 1.015")
	require.Equal(t, 0, spec.Attempt)
	require.True(t, strings.HasSuffix(spec.ClientOrderID, "a0"), spec.ClientOrderID)
}

func TestPseudoMarketSellRoundsDownAndAnOrderModeSwitchGivesPlainMarket(t *testing.T) {
	f := swapPolicyFixture(t, pseudoSettings)
	f.position(t, "account-a", "BTCUSDT", "10")
	f.target(t, nil)
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "98.5", f.orders.specs[0].LimitPrice.String(), "100 × 0.985")
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)

	market := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0"}`)
	market.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	market.converge(t)
	require.Equal(t, exchange.OrderTypeMarket, market.orders.specs[0].Type)
	require.Nil(t, market.orders.specs[0].LimitPrice)
}

func TestPseudoMarketPriceRoundingFollowsThePriceTick(t *testing.T) {
	settings := ordersettings.Default()
	// 99.01 × 1.015 = 100.49515，买入向上取整到 0.1 → 100.5；99.01 × 0.985 = 97.52485，卖出向下 → 97.5。
	require.Equal(t, "100.5", pseudoMarketPrice(shared.MustDecimal("99.01"), exchange.SideBuy, settings, 0, "0.1").String())
	require.Equal(t, "97.5", pseudoMarketPrice(shared.MustDecimal("99.01"), exchange.SideSell, settings, 0, "0.1").String())
	// 正好在刻度上不多不少。
	require.Equal(t, "101.5", pseudoMarketPrice(shared.MustDecimal("100"), exchange.SideBuy, settings, 0, "0.1").String())
	// 价格精度不明时不取整。
	require.Equal(t, "100.49515", pseudoMarketPrice(shared.MustDecimal("99.01"), exchange.SideBuy, settings, 0, "").String())
	// 滑点逐次放大并封顶。
	require.Equal(t, "106", pseudoMarketPrice(shared.MustDecimal("100"), exchange.SideBuy, settings, 9, "0.1").String())
}

// 部分成交时只补剩余量，滑点按第二次重算；单号带尝试次数。
func TestPseudoMarketRetryOrdersOnlyTheRemainderWithWiderSlippage(t *testing.T) {
	f := swapPolicyFixture(t, pseudoSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.iocChild(t, "ioc-0", "10", "4", 0)
	f.position(t, "account-a", "BTCUSDT", "4")
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.Equal(t, "6", spec.Quantity.String(), "只补剩余的 6")
	require.Equal(t, "103", spec.LimitPrice.String(), "100 × (1 + 2 × 1.5%)")
	require.Equal(t, 1, spec.Attempt)
	require.True(t, strings.HasSuffix(spec.ClientOrderID, "a1"), spec.ClientOrderID)
}

func TestPseudoMarketAttemptsCountOnlyTheTrailingUnfilledRun(t *testing.T) {
	child := func(tif, quantity, filled string) store.TargetChildOrder {
		return store.TargetChildOrder{OrderType: "LIMIT", TimeInForce: tif, Quantity: quantity, FilledQuantity: filled}
	}
	require.Equal(t, 0, pseudoMarketAttempts(nil))
	require.Equal(t, 3, pseudoMarketAttempts([]store.TargetChildOrder{child("IOC", "5", "0"), child("IOC", "5", "2"), child("IOC", "3", "0")}))
	// 一笔全部成交就清零：拆单的每一片都从首次滑点开始。
	require.Equal(t, 0, pseudoMarketAttempts([]store.TargetChildOrder{child("IOC", "5", "0"), child("IOC", "5", "0"), child("IOC", "5", "5")}))
	require.Equal(t, 1, pseudoMarketAttempts([]store.TargetChildOrder{child("IOC", "5", "5"), child("IOC", "5", "1")}))
	// 市价单、挂单不是伪市价重试，打断连续。
	require.Equal(t, 1, pseudoMarketAttempts([]store.TargetChildOrder{child("IOC", "5", "0"), {OrderType: "MARKET", Quantity: "1", FilledQuantity: "1"}, child("IOC", "5", "0")}))
	require.Equal(t, 0, pseudoMarketAttempts([]store.TargetChildOrder{child("IOC", "5", "0"), child("GTC", "5", "0")}))
}

// 用满次数仍没成交完：记为未到位，不再下单，告警一次；不卡住别的标的。
func TestPseudoMarketExhaustedAttemptsAreUnfilledAndAlertedOnce(t *testing.T) {
	f := swapPolicyFixture(t, pseudoSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	for attempt := 0; attempt < 4; attempt++ {
		// 两个成员都用满了次数：换成员也没用。
		f.iocChild(t, "ioc-a"+string(rune('a'+attempt)), "10", "0", attempt)
		f.iocChildOn(t, "account-b", "OKX", "BTC-USDT-SWAP", "ioc-b"+string(rune('a'+attempt)), "10", "0", attempt)
	}
	executor := f.executor()
	executor.MinBatchInterval = 0
	for round := 0; round < 3; round++ {
		result, err := executor.Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status, "%+v", result)
	}
	require.Empty(t, f.orders.specs)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Contains(t, target.BlockedTargets[0].Reason, "未到位")
	require.Len(t, executor.warned, 1, "每轮都会再判到，告警只发一次")
}

func TestPseudoMarketExhaustedLaneDoesNotStallLaterStages(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setOrderSettings(t, `{"min_notional_swap":"0","min_notional_spot":"0"}`)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
	})
	for attempt := 0; attempt < 4; attempt++ {
		f.iocChild(t, "ioc-"+string(rune('a'+attempt)), "10", "0", attempt)
	}
	f.now = f.now.Add(0)
	result := f.converge(t)
	require.Equal(t, []string{"BTC-USDT-SPOT:BUY"}, placedInstruments(f), "合约未到位，现货买入照常下")
	require.Equal(t, StatusConverging, result.Status)
}

// 结果不明（提交中、提交结果未知）的伪市价单先查单，确认结果前不重下。
func TestPseudoMarketUnknownResultIsResolvedBeforeReplacing(t *testing.T) {
	f := swapPolicyFixture(t, pseudoSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "unknown-ioc", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "unknown-ioc", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "IOC", Side: "BUY", Quantity: "10", LimitPrice: strPtr("101.5"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "target-current", State: string(orderdomain.SubmitUnknown), SubmittedAt: 1_000, Version: 1,
	})
	result := f.converge(t)
	require.Equal(t, "wait", result.Action)
	require.Equal(t, []string{"unknown-ioc"}, f.orders.resolved, "先按订单号查单")
	require.Empty(t, f.orders.specs, "没确认结果前不重下")
}

// 现货卖出不超过可卖余额：钱包里 10 个 ETH 只有 4 个可卖。
func TestPseudoMarketSpotSellNeverExceedsSellableBalance(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setOrderSettings(t, pseudoSettings)
	account, err := f.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	for index := range account.Snapshot.Balances {
		if account.Snapshot.Balances[index].Asset == "ETH" {
			account.Snapshot.Balances[index].Available = "4"
		}
	}
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: 1_900, LastSyncAt: 1_900,
		})
	}))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "0"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "4", f.orders.specs[0].Quantity.String())
	require.Equal(t, exchange.FillPolicyIOC, f.orders.specs[0].FillPolicy)
}
