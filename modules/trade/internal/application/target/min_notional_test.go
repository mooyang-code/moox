package target

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// minNotionalFixture 把两个成员的标的都设成：交易所最小名义金额 5、数量步长 0.0001（合约面值 0.001 × 0.1 张）。
func minNotionalFixture(t *testing.T, settings string) *targetFixture {
	t.Helper()
	f := swapPolicyFixture(t, settings)
	f.setQuantityRule(t, "BINANCE", "BTCUSDT", "0.1", "0.1")
	f.setQuantityRule(t, "OKX", "BTC-USDT-SWAP", "0.1", "0.1")
	f.setMinNotional(t, "BINANCE", "BTCUSDT", "5")
	f.setMinNotional(t, "OKX", "BTC-USDT-SWAP", "5")
	return f
}

// 伪市价加空：差额 5.05U，参考价下够 5U，但限价比参考价低 1.5%，按限价只有约 4.97U。
// 按限价预检：算作差额太小、视为已满足，不下单、不报阻塞（以前是下单后被校验器拒掉，写成“成员容量不足”并告警）。
func TestPseudoMarketShortBelowMinimumNotionalAtLimitPriceIsSatisfied(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`)
	f.position(t, "account-a", "BTCUSDT", "-10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-10.0505"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, latestBlocked(t, f))
}

// 同样的差额，限价够 5U 时照常下单（限价 = 参考价 × 0.985，7U 的差额按限价还有 6.9U）。
func TestPseudoMarketShortAboveMinimumNotionalAtLimitPriceIsPlaced(t *testing.T) {
	f := minNotionalFixture(t, `{"min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`)
	f.position(t, "account-a", "BTCUSDT", "-10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-10.07"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "98.5", f.orders.specs[0].LimitPrice.String())
}

// 按可用资金截量后低于设置的最小下单额：按资金不足处理，不下单，阻塞原因讲资金不足而不是“成员容量不足”。
func TestCapacityClippedBelowConfiguredMinimumIsFundsShortage(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"50"}`)
	f.orders.capacities = map[string]shared.Decimal{"account-a": shared.MustDecimal("0.1"), "account-b": shared.MustDecimal("0.1")}
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	result := f.converge(t)
	require.Empty(t, f.orders.submitted, "截量后只有 10U，低于设置的最小下单额 50U，不能下")
	require.Equal(t, StatusBlocked, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Contains(t, blocked[0].Reason, "资金不足")
	require.NotContains(t, blocked[0].Reason, "成员容量不足")
}

// 单笔上限 3U、标的最小下单额 5U：子单被截到不足最小下单额，阻塞原因直接说是单笔上限的问题。
func TestChildLimitBelowMinimumNotionalExplainsTheSetting(t *testing.T) {
	f := minNotionalFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}})
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusBlocked, result.Status)
	blocked := latestBlocked(t, f)
	require.Len(t, blocked, 1)
	require.Equal(t, "单笔上限 3 低于该标的最小下单额 5，请调高单笔上限", blocked[0].Reason)
}
