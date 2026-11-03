package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func filledChild(id, owner, ownerType, side, before string, finishedAt int64) store.OrderRecord {
	record := store.OrderRecord{
		SpaceID: "space-1", OrderID: id, TradingAccountID: "account-a", ClientOrderID: id,
		Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: side, PositionSide: "NET", Quantity: "1",
		ReferencePrice: "100", ReferencePriceAt: 1_000,
		OwnerType: ownerType, OwnerID: owner, LogicalAccountID: "logical-1",
		PositionBefore: before, State: "FILLED", FilledQuantity: "1",
		SubmittedAt: finishedAt - 10, FinishedAt: finishedAt, Version: 1,
	}
	if ownerType == "TARGET" {
		record.RunnerID = "runner-1"
	}
	return record
}

func latestBlocked(t *testing.T, f *targetFixture) []store.BlockedTarget {
	t.Helper()
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return target.BlockedTargets
}

func TestRebalanceThresholdSkipsSmallSameSignDifference(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "97")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, f.orders.specs)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	has, err := f.executor().HasDifference(context.Background(), target)
	require.NoError(t, err)
	require.False(t, has, "HasDifference 必须和阈值一致")
}

func TestRebalanceThresholdDoesNotSkipSmallOppositePosition(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "-3")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, exchange.SideBuy, f.orders.specs[0].Side)
	require.True(t, f.orders.specs[0].ReducePositionOnly)
}

func TestRebalanceThresholdDoesNotSkipMemberHedge(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "150")
	f.position(t, "account-b", "BTC-USDT-SWAP", "-48")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1, "合计只差 2%% 也要先平掉反向成员")
}

func TestRebalanceThresholdNeverSkipsFlatten(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "2")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
}

func TestPlaceRecordsPositionBeforeOfPlacingMember(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "3"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "account-a", f.orders.specs[0].TradingAccountID)
	require.Equal(t, "1", f.orders.specs[0].PositionBefore)
}

// 持仓回退到已成交子单下单前的值：整条标的跳过，不换成员。
func TestUnreflectedPositionSkipsWholeLane(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.orders.capacities = map[string]shared.Decimal{"account-a": shared.Zero()}
	f.order(t, filledChild("old-buy", "target-old", "TARGET", "BUY", "0", 1_500))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs, "成员 B 不能替回退的成员 A 下单")
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, reasonPositionUnreflected, blocked[0].Reason)
}

func TestUnreflectedPositionClearsAfterOperatorFill(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.order(t, filledChild("old-buy", "target-old", "TARGET", "BUY", "0", 1_500))
	f.order(t, filledChild("flatten-sell", "action-1", "OPERATOR", "SELL", "", 1_600))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1, "清仓后恢复重新开仓不能被挡")
}

func TestUnreflectedPositionIgnoresReflectedFills(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.order(t, filledChild("first-buy", "target-old", "TARGET", "BUY", "0", 1_500))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "1", f.orders.specs[0].PositionBefore)
}

func TestUnreflectedPositionAllowsFlipSellThenSell(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	// 上一个目标从 0 买到 1；本目标卖平到 0，接着要卖开到 -1。
	f.order(t, filledChild("old-buy", "target-old", "TARGET", "BUY", "0", 1_400))
	f.order(t, filledChild("close-sell", "target-current", "TARGET", "SELL", "1", 1_500))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-1"}})
	_, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1, "翻转卖开不能被上一个目标的买单误挡")
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)
}

func TestSecondReversalWithinTargetSkipsLane(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.order(t, filledChild("buy-1", "target-current", "TARGET", "BUY", "0", 1_400))
	f.order(t, filledChild("sell-1", "target-current", "TARGET", "SELL", "2", 1_500))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "3"}})
	result, err := f.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, reasonTargetReversal, blocked[0].Reason)
}

func TestFirstReversalWithinTargetIsAllowedAndReported(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "3")
	f.order(t, filledChild("buy-1", "target-current", "TARGET", "BUY", "1", 1_500))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	executor := f.executor()
	var reported []string
	executor.OnReversal = func(_, logical, instrument string, _ time.Time) { reported = append(reported, logical+"/"+instrument) }
	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)
	require.Equal(t, []string{"logical-1/BTC-USDT-SWAP"}, reported)
}

// 多成员一多一空：反向按执行账户计，不同账户的买和卖不算反向。
func TestReversalCountedPerMember(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.position(t, "account-a", "BTCUSDT", "2")
	// 成员 B 本目标内卖平了 1，持仓已反映（1→0）；成员 A 接着买入不算反向。
	record := filledChild("b-sell", "target-current", "TARGET", "SELL", "1", 1_500)
	record.TradingAccountID, record.ClientOrderID, record.Exchange, record.ExchangeSymbol = "account-b", "b-sell", "OKX", "BTC-USDT-SWAP"
	f.order(t, record)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "3"}})
	executor := f.executor()
	reported := 0
	executor.OnReversal = func(string, string, string, time.Time) { reported++ }
	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Zero(t, reported)
}
