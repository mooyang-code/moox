package target

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 单笔上限把子单截到贴着或低于交易所最小名义金额时，执行器照常下单，是否合规交给下单校验器判断
// （不合规的会被校验器拒掉并写阻塞原因）：这些场景不能被执行器自己当成“已满足”静默收敛。
func TestSliceLimitNearMinimumNotionalIsNeverSilentlyConverged(t *testing.T) {
	swap := func(t *testing.T, settings string) *targetFixture {
		f := swapPolicyFixture(t, settings)
		f.setQuantityRule(t, "BINANCE", "BTCUSDT", "0.1", "0.1")
		f.setQuantityRule(t, "OKX", "BTC-USDT-SWAP", "0.1", "0.1")
		f.setMinNotional(t, "BINANCE", "BTCUSDT", "5")
		f.setMinNotional(t, "OKX", "BTC-USDT-SWAP", "5")
		return f
	}
	cases := map[string]func(t *testing.T) *targetFixture{
		"合约全平，单笔上限 3U": func(t *testing.T) *targetFixture {
			f := swap(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3"}`)
			f.position(t, "account-a", "BTCUSDT", "1")
			f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0"}})
			return f
		},
		"合约减仓 1 到 0.5，单笔上限 3U": func(t *testing.T) *targetFixture {
			f := swap(t, `{"order_mode":"MARKET","min_notional_swap":"0","max_child_notional":"3"}`)
			f.position(t, "account-a", "BTCUSDT", "1")
			f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.5"}})
			return f
		},
		"现货清仓，单笔上限 3U": func(t *testing.T) *targetFixture {
			f := newTargetFixture(t, exchange.MarketTypeSpot)
			f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","max_child_notional":"3"}`)
			f.setQuantityRule(t, "BINANCE", "BTCUSDT", "0.01", "0.01")
			f.setMinNotional(t, "BINANCE", "BTCUSDT", "5")
			f.setBalance(t, "account-a", "BTC", "1")
			f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0"}})
			return f
		},
		"伪市价加空，单笔上限 5.04U": func(t *testing.T) *targetFixture {
			f := swap(t, `{"min_notional_swap":"0","max_child_notional":"5.04","rebalance_threshold_mode":"ALWAYS"}`)
			f.position(t, "account-a", "BTCUSDT", "-10")
			f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "-20"}})
			return f
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			result := f.converge(t)
			require.Len(t, f.orders.specs, 1)
			require.NotEqual(t, StatusConverged, result.Status)
		})
	}
}
