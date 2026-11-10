package target

import (
	"testing"
	"time"

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

// 开了拆单随机化，4.9 的 50%～100% 一定不足 5U，配置的单笔上限本身也不够：抬不起来，阻塞，原因显示配置值。
func TestChildLimitReasonShowsConfiguredValueUnderRandomizedSplit(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"4.9","randomize_split":true}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	f.converge(t)
	require.Empty(t, f.orders.specs)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 4.9 低于该标的最小下单额 5，请调高单笔上限", blocked[0].Reason)
}

// 拆单随机化抽到偏低的一笔（这个种子第 0 笔抽到 0.661，12.1 × 0.661 ≈ 7.99U，不够设置的 8U），而配置的单笔上限本身够：
// 抬到刚好 8U 下单，不阻塞。
func TestRandomizedSliceBelowConfiguredMinimumIsLiftedToIt(t *testing.T) {
	require.Equal(t, "0.661", splitFraction("logical-1|target-current|BTC-USDT-SWAP|0").String(), "测试依赖这个种子的抽样结果")
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"8","max_child_notional":"12.1","randomize_split":true}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "0.08", f.orders.specs[0].Quantity.String(), "抬到 8U / 价格 100")
	require.Empty(t, latestBlocked(t, f))
}

// 被单笔上限截小后不够设置的最小下单额，而配置的单笔上限本身也不够：阻塞，建议调高单笔上限或降低最小下单额。
func TestChildLimitBelowConfiguredMinimumExplainsTheSetting(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"50","max_child_notional":"30"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusBlocked, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 30 低于设置的最小下单额 50，请调高单笔上限或降低最小下单额", blocked[0].Reason)
}

// 截量前的差额按下单价本来就不够（5.05U 的差额，限价下约 4.97U）：单笔上限再低也只是“差额太小、视为已满足”，不阻塞。
func TestSmallDifferenceIsSatisfiedEvenWhenSliceLimitClipsIt(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","max_child_notional":"5.04","rebalance_threshold_mode":"ALWAYS"}`)
	f.position(t, "account-a", "BTCUSDT", "-10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-10.0505"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, latestBlocked(t, f))
}

// 被每小时上限截小：按下单价不够交易所最小名义金额时顺延到额度恢复，不能算“差额太小”而静默收敛。
func TestHourlyCapClippedBelowMinimumAtLimitPriceDefers(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS","hourly_cap_swap":{"BTC-USDT-SWAP":"5.04"}}`)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "-10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-20"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status)
	require.Positive(t, result.RetryAfter, "设好到下一个整点的唤醒")
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.True(t, blocked[0].Deferred)
	require.Equal(t, reasonHourlyCapDeferred, blocked[0].Reason)
}

// 同样的顺延，按下单价不够的是设置的最小下单额（新开仓也一样）。
func TestHourlyCapClippedBelowConfiguredMinimumAtLimitPriceDefers(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"8","hourly_cap_swap":{"BTC-USDT-SWAP":"8.05"}}`)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-1"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.True(t, blocked[0].Deferred)
	require.Equal(t, reasonHourlyCapDeferred, blocked[0].Reason)
}

// 合约只减仓单在伪市价限价下被单笔上限截小：不查交易所最小名义金额（5.04U 的单，限价下约 4.96U），照常下单。
func TestPseudoMarketReduceClippedBySliceLimitIsExemptFromMinimumNotional(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","max_child_notional":"5.04"}`)
	f.position(t, "account-a", "BTCUSDT", "1")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.5"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.True(t, spec.ReducePositionOnly)
	require.Equal(t, exchange.FillPolicyIOC, spec.FillPolicy)
	require.Equal(t, "0.0504", spec.Quantity.String())
}
