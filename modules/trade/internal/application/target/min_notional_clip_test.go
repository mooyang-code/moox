package target

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 单笔上限 3U、交易所最小名义金额 5U，合约全平：只减仓单不受最小名义金额限制，照常下 3U 的一笔，不阻塞。
func TestSwapCloseClippedBySliceLimitStillPlaced(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3"}`)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0"}})
	result := f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.True(t, f.orders.specs[0].ReducePositionOnly)
	require.Equal(t, "0.03", f.orders.specs[0].Quantity.String())
	require.Equal(t, StatusConverging, result.Status)
	require.Empty(t, latestBlocked(t, f))
}

// 合约部分减仓同理：被单笔上限截小的只减仓单照常下。
func TestSwapPartialReduceClippedBySliceLimitStillPlaced(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3"}`)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.5"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "0.03", f.orders.specs[0].Quantity.String())
	require.Empty(t, latestBlocked(t, f))
}

// 现货卖出没有只减仓豁免：清仓子单被单笔上限截到不足最小下单额，阻塞并写清是单笔上限的问题，不能静默跳过。
func TestSpotSellClippedBelowMinimumNotionalIsBlockedWithReason(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSpot)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","max_child_notional":"3"}`)
	f.setQuantityRule(t, "BINANCE", "BTCUSDT", "0.01", "0.01")
	f.setQuantityRule(t, "OKX", "BTC-USDT-SWAP", "0.01", "0.01")
	f.setMinNotional(t, "BINANCE", "BTCUSDT", "5")
	f.setMinNotional(t, "OKX", "BTC-USDT-SWAP", "5")
	f.setBalance(t, "account-a", "BTC", "1")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusBlocked, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 3 低于该标的最小下单额 5，请调高单笔上限", blocked[0].Reason)
}

// 截量和限价叠加：伪市价加空差额 1000U，单笔上限 5.04U 按参考价够 5U，按限价（参考价 × 0.985）只有约 4.96U。
// 这是单笔上限设得太低，要阻塞并写清原因，不能当成“差额太小、视为已满足”而静默跳过。
func TestPseudoMarketAddShortClippedThenLimitPriceBelowMinimumIsBlocked(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","max_child_notional":"5.04","rebalance_threshold_mode":"ALWAYS"}`)
	f.position(t, "account-a", "BTCUSDT", "-10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-20"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusBlocked, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 5.04 按下单价折算后低于该标的最小下单额 5，请调高单笔上限", blocked[0].Reason)
}

// 开了拆单随机化：原因里显示配置值，并说明每笔只有单笔上限的 50%～100%（4.9 的 50%～100% 一定不足 5U）。
func TestChildLimitReasonShowsConfiguredValueUnderRandomizedSplit(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"4.9","randomize_split":true}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	f.converge(t)
	require.Empty(t, f.orders.specs)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 4.9 低于该标的最小下单额 5，请调高单笔上限（开了拆单随机化，每笔只有单笔上限的 50%～100%）", blocked[0].Reason)
}
