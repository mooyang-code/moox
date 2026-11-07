package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func (f *targetFixture) block(t *testing.T, market, symbol string) {
	t.Helper()
	_, err := f.store.BlockInstrument(context.Background(), store.BlockedInstrumentRecord{
		Exchange: "BINANCE", MarketType: market, ExchangeSymbol: symbol, Reason: "测试下架", BlockedAt: 1,
	})
	require.NoError(t, err)
}

func (f *targetFixture) delistInstrument(t *testing.T, instrumentID string) {
	t.Helper()
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_status = 'BREAK' WHERE c_instrument_id = ?", instrumentID).Error)
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
	f.block(t, "SWAP", "BTCUSDT")

	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	result := f.converged(t)
	require.Empty(t, f.orders.specs, "加仓被截掉")
	require.Equal(t, StatusConverged, result.Status)
}

func TestBlockedInstrumentStillAllowsReducing(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "SWAP", "BTCUSDT")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f), "策略主动减仓照常执行")
	require.Equal(t, "3", f.orders.specs[0].Quantity.String())
}

// 反向目标：多头持仓遇到空头目标只平到 0，不会反向开仓。
func TestBlockedInstrumentReversalOnlyCloses(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "SWAP", "BTCUSDT")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-3"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f))
	require.Equal(t, "5", f.orders.specs[0].Quantity.String(), "只平掉现有的 5，不开空")
}

// 隐式目标 0：受管持仓不在目标里，名单标的照常按 0 卖出。
func TestBlockedInstrumentNotInTargetIsStillSold(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "SWAP", "BTCUSDT")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "10"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL"}, placedInstruments(f))
}

// 别的交易所的名单不影响这个账户。
func TestBlockedInstrumentOnOtherExchangeDoesNotRestrict(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	_, err := f.store.BlockInstrument(context.Background(), store.BlockedInstrumentRecord{
		Exchange: "OKX", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", Reason: "别家", BlockedAt: 1,
	})
	require.NoError(t, err)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"}})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f))
}

// 现货只减不增，同名永续不受影响。
func TestBlockedSpotDoesNotRestrictSwap(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "SPOT", "BTCUSDT")
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "3"},
	})
	f.converged(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f), "现货买入被截掉，永续加仓照常")
}

// 已经不能交易的永续：持仓不再让目标置为 BLOCKED，目标里的这一项整项跳过，其余照常。
func TestBlockedUntradableSwapDoesNotBlockTarget(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.delistInstrument(t, "BTC-USDT-SWAP")
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})

	// 没进名单：持仓没有可交易标的，目标被阻塞。
	result := f.converged(t)
	require.Equal(t, StatusBlocked, result.Status)

	f.block(t, "SWAP", "BTCUSDT")
	result = f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status, "名单里的不再阻塞组合")
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f), "名单标的不买不卖，其余照常")
}

// 现货按基础资产匹配，而且只匹配 SPOT 条目：永续下架不会把同名现货余额的阻塞也豁免掉。
func TestBlockedSpotBalanceIsMatchedByBaseAssetAndSpotEntriesOnly(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSpotBalance("BTC", "2")
	f.delistInstrument(t, "BTC-USDT-SPOT")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Hold: true}})

	f.block(t, "SWAP", "BTCUSDT")
	result := f.converged(t)
	require.Equal(t, StatusBlocked, result.Status, "永续条目不豁免现货余额")

	f.block(t, "SPOT", "BTCUSDT")
	result = f.converged(t)
	require.NotEqual(t, StatusBlocked, result.Status)
}

// 下架清仓正在做：它的 OPERATOR 单不算冲突，不暂停组合；执行器不碰这个标的，别的标的照常。
func TestRunningDelistFlattenIsNotAConflictAndIsSkipped(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setSwapPosition("BTCUSDT", "5")
	f.block(t, "SWAP", "BTCUSDT")
	_, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "delist-1", LogicalAccountID: "logical-1", ActionType: "DELIST_FLATTEN",
		Reason: "下架", RequestJSON: `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"BINANCE"}`,
		Status: "RUNNING",
	})
	require.NoError(t, err)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "flatten-child", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "flatten-child", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "5", ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: "delist-1", State: "OPEN", Version: 1,
	})
	// 目标要求加到 20：既不能加仓（名单），也不能因为清仓单暂停组合。
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "20"},
		{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"},
	})

	result := f.converged(t)
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState, "下架清仓的单不暂停组合")
	require.NotEqual(t, StatusPaused, result.Status)
	require.Equal(t, []string{"ETH-USDT-SPOT:BUY"}, placedInstruments(f), "清仓中的标的不碰，别的标的照常")
}

// 对照：别的 OPERATOR 单仍然算冲突，暂停组合。
func TestOtherOperatorOrdersStillPauseTheLogicalAccount(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "manual-child", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "manual-child", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "5", ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: "manual-1", State: "OPEN", Version: 1,
	})
	f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}})
	result := f.converged(t)
	require.Equal(t, StatusPaused, result.Status)
}
