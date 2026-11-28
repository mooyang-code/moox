package target

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store/storetest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func (f *targetFixture) block(t *testing.T, market, symbol string) {
	t.Helper()
	_, err := storetest.BlockInstrument(f.store, store.BlockedInstrumentRecord{
		Exchange: "binance", MarketType: market, ExchangeSymbol: symbol, Reason: "测试下架", BlockedAt: 1,
	})
	require.NoError(t, err)
}

func (f *targetFixture) delistInstrument(t *testing.T, instrumentID string) {
	t.Helper()
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_status = 'BREAK' WHERE c_instrument_id = ?", instrumentID).Error)
	f.store.InvalidateCaches()
}

func (f *targetFixture) resumeInstrument(t *testing.T, instrumentID string) {
	t.Helper()
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_status = 'TRADING' WHERE c_instrument_id = ?", instrumentID).Error)
	f.store.InvalidateCaches()
}

func (f *targetFixture) removeInstrument(t *testing.T, instrumentID string) {
	t.Helper()
	require.NoError(t, f.store.DBForTest().Exec("DELETE FROM t_trade_instruments WHERE c_instrument_id = ?", instrumentID).Error)
	f.store.InvalidateCaches()
}

func (f *targetFixture) converged(t *testing.T) Result {
	t.Helper()
	f.now = time.Now().UTC()
	return f.converge(t)
}

func TestCapReduceOnly(t *testing.T) {
	for _, c := range []struct{ target, position, want string }{
		{"20", "5", "5"},    // 加仓去掉
		{"3", "5", "3"},     // 减仓照常
		{"5", "5", "5"},     // 持平
		{"-3", "5", "0"},    // 反向开仓的部分去掉，只平到 0
		{"20", "0", "0"},    // 没有持仓不能开仓
		{"-20", "-5", "-5"}, // 空头加仓去掉
		{"-2", "-5", "-2"},  // 空头减仓照常
		{"2", "-5", "0"},
		{"0", "5", "0"},
	} {
		got := capReduceOnly(shared.MustDecimal(c.target), shared.MustDecimal(c.position))
		require.Equal(t, c.want, got.String(), "目标 %s 持仓 %s", c.target, c.position)
	}
}

// 名单里还能交易的标的只减不增：目标比持仓大时不加仓，目标比持仓小照常减仓。
func TestBlockedTradableInstrumentIsReduceOnly(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")

	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	result := f.converged(t)
	require.Empty(t, f.orders.specs, "加仓被截掉")
	require.Equal(t, StatusConverged, result.Status)
}

func TestBlockedInstrumentStillAllowsReducing(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f), "策略主动减仓照常执行")
	require.Equal(t, "3", f.orders.specs[0].Quantity.String())
}

// 反向目标：多头持仓遇到空头目标只平到 0，不会反向开仓。
func TestBlockedInstrumentReversalOnlyCloses(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-3"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f))
	require.Equal(t, "5", f.orders.specs[0].Quantity.String(), "只平掉现有的 5，不开空")
}

// 隐式目标 0：受管持仓不在目标里，名单标的照常按 0 卖出。
func TestBlockedInstrumentNotInTargetIsStillSold(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f))
}

// 别的交易所的名单不影响这个账户。
func TestBlockedInstrumentOnOtherExchangeDoesNotRestrict(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	_, err := storetest.BlockInstrument(f.store, store.BlockedInstrumentRecord{
		Exchange: "okx", MarketType: "swap", ExchangeSymbol: "BTCUSDT", Reason: "别家", BlockedAt: 1,
	})
	require.NoError(t, err)
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f))
}

// 现货只减不增，同名永续不受影响。
func TestBlockedSpotDoesNotRestrictSwap(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "spot", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
	})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f), "现货买入被截掉，永续加仓照常")
}

// 名单里已经不能交易的永续：持仓不让目标置为 BLOCKED，目标里的这一项整项跳过，其余照常。
func TestBlockedUntradableSwapDoesNotBlockTarget(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.delistInstrument(t, "BTC-USDT-SWAP")
	f.block(t, "swap", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})
	result := f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status, "名单里的不阻塞组合")
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f), "名单标的不买不卖，其余照常")
}

// 名单里已经不能交易的现货：余额按基础资产匹配，整项跳过，不阻塞组合。
func TestBlockedUntradableSpotBalanceDoesNotBlockTarget(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSpotBalance("BTC", "2")
	f.delistInstrument(t, "BTC-USDT-SPOT")
	f.block(t, "spot", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})
	result := f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status)
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f))
}

// 列表里有、但暂不可交易（BREAK）而且不在名单里的现货余额和永续持仓：本轮跳过，不阻塞组合，其余照常收敛；
// 恢复 TRADING 后照常收敛。
func TestSuspendedHoldingsAreSkippedUntilTradableAgain(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSpotBalance("BTC", "2")
	f.setSwapPosition("BTCUSDT", "5")
	f.delistInstrument(t, "BTC-USDT-SPOT")
	f.delistInstrument(t, "BTC-USDT-SWAP")
	f.target(t, []store.InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})
	result := f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status, "暂不可交易的持仓不阻塞组合")
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f), "暂停的标的不买不卖，其余照常")
	stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, stored.BlockedTargets)

	// 恢复可交易：ETH 已成交，暂停期间跳过的两项照常收敛。
	f.orders.specs = nil
	f.setSpotBalance("ETH", "12")
	f.resumeInstrument(t, "BTC-USDT-SPOT")
	f.resumeInstrument(t, "BTC-USDT-SWAP")
	e := f.executor()
	e.MinBatchInterval = 0
	result, err = e.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.NotEqual(t, StatusBlocked, result.Status)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f), "先做合约加仓，现货买入在后一阶段")
	require.Equal(t, "5", f.orders.specs[0].Quantity.String())
}

// 目标里对暂不可交易的现货是保持（Hold）：同样不阻塞组合。
func TestSuspendedSpotHoldDoesNotBlockTarget(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSpotBalance("BTC", "2")
	f.delistInstrument(t, "BTC-USDT-SPOT")
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SPOT", Hold: true}})
	result := f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status)
	require.Empty(t, f.orders.specs)
}

// 交易所列表里没有对应标的的持仓：照旧阻塞组合。现货沿用结算币过滤：只有别的结算币的、暂不可交易的标的，
// 不算列表里有；永续是标的整条不在列表里。
func TestHoldingsWithoutListedInstrumentStillBlockTarget(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, f *targetFixture)
	}{
		{"现货余额", func(t *testing.T, f *targetFixture) {
			f.setSpotBalance("BTC", "2")
			f.delistInstrument(t, "BTC-USDT-SPOT")
			require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_settlement_asset = 'FDUSD' WHERE c_instrument_id = 'BTC-USDT-SPOT'").Error)
			f.store.InvalidateCaches()
		}},
		{"永续持仓", func(t *testing.T, f *targetFixture) {
			f.setSwapPosition("BTCUSDT", "5")
			f.removeInstrument(t, "BTC-USDT-SWAP")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newMixedExecutorFixture(t)
			c.setup(t, f)
			// 现货余额要在目标里才受管；永续持仓不论在不在目标里都要处理。
			f.target(t, []store.InstrumentQuantity{
				{InstrumentID: "BTC-USDT-SPOT", Hold: true},
				{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
			})
			result := f.converged(t)
			require.Equal(t, StatusBlocked, result.Status)
			stored, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Len(t, stored.BlockedTargets, 1)
			require.Contains(t, stored.BlockedTargets[0].Reason, "可交易")
		})
	}
}

// 下架清仓正在做：它的 OPERATOR 单不算冲突，不暂停组合；执行器不碰这个标的，别的标的照常。
func TestRunningDelistFlattenIsNotAConflictAndIsSkipped(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")
	_, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "delist-1", LogicalAccountID: "logical-1", ActionType: "DELIST_FLATTEN",
		Reason: "下架", RequestJSON: `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"binance"}`,
		Status: "RUNNING",
	})
	require.NoError(t, err)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "flatten-child", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "flatten-child", Exchange: "binance", MarketType: "swap", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "5", ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: "delist-1", State: "OPEN", Version: 1,
	})
	// 目标要求加到 20：既不能加仓（名单），也不能因为清仓单暂停组合。
	f.target(t, []store.InstrumentQuantity{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})

	result := f.converged(t)
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState(), "下架清仓的单不暂停组合")
	require.NotEqual(t, StatusPaused, result.Status)
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f), "清仓中的标的不碰，别的标的照常")
}

// 对照：别的 OPERATOR 单仍然算冲突，暂停组合。
func TestOtherOperatorOrdersStillPauseTheLogicalAccount(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "manual-child", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "manual-child", Exchange: "binance", MarketType: "swap", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "5", ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: "manual-1", State: "OPEN", Version: 1,
	})
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}})
	result := f.converged(t)
	require.Equal(t, StatusPaused, result.Status)
}

func reduceOnlyWarnings(executor *Executor) int {
	count := 0
	for key := range executor.warned {
		if strings.HasPrefix(key, "reduce-only|") {
			count++
		}
	}
	return count
}

// 截断告警只在确实截掉了会下出的买入量时发：目标只比持仓多一点点（低于调仓阈值，本来也不会下单）不告警。
func TestReduceOnlyWarningOnlyWhenTheTruncatedBuyWouldHaveBeenPlaced(t *testing.T) {
	for _, c := range []struct {
		target string
		warns  int
	}{{"20", 1}, {"5.1", 0}, {"5", 0}} {
		f := newMixedExecutorFixture(t)
		f.setSwapPosition("BTCUSDT", "5")
		f.block(t, "swap", "BTCUSDT")
		f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: c.target}})
		f.now = time.Now().UTC()
		executor := f.executor()
		executor.MinBatchInterval = 0
		_, err := executor.Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err)
		require.Empty(t, f.orders.specs, "目标 %s：加仓都被截掉", c.target)
		require.Equal(t, c.warns, reduceOnlyWarnings(executor), "目标 %s", c.target)
	}
}

type countingPrices struct {
	inner PriceSource
	calls *int
}

func (c countingPrices) LatestPrice(ctx context.Context, accountID string, key exchange.SymbolKey) (Quote, error) {
	*c.calls++
	return c.inner.LatestPrice(ctx, accountID, key)
}

// 当天已经告警过的标的，之后每一轮都不再为了判断“要不要告警”去取报价。
func TestReduceOnlyWarningChecksDedupeBeforeFetchingAQuote(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "swap", "BTCUSDT")
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	f.now = time.Now().UTC()
	calls := 0
	executor := f.executor()
	executor.MinBatchInterval = 0
	executor.Prices = countingPrices{inner: executor.Prices, calls: &calls}

	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, 1, reduceOnlyWarnings(executor))
	require.Equal(t, 1, calls, "第一次判断取一次报价")

	for i := 0; i < 3; i++ {
		_, err = executor.Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls, "已经告警过，之后不再取报价")
}

// 演练的名单行（仅模拟盘）对模拟盘组合同样只减不增。
func TestPaperOnlyBlockRestrictsPaperMembers(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	_, err := storetest.BlockInstrument(f.store, store.BlockedInstrumentRecord{
		Exchange: "binance", MarketType: "swap", ExchangeSymbol: "BTCUSDT", Scope: store.DelistScopePaperOnly, Reason: "演练", BlockedAt: 1,
	})
	require.NoError(t, err)
	f.target(t, []store.InstrumentQuantity{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	f.converged(t)
	require.Empty(t, f.orders.specs, "模拟盘组合加仓被截掉")
}
