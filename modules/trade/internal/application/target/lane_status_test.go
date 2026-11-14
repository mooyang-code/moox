package target

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

const swapID = "BTC-USDT-SWAP"

// defaultOrderSettings 只写下单方式，其余都取默认值：现货最小下单额 10、合约 5、按目标数量 5% 的调仓阈值。
const defaultOrderSettings = `{"order_mode":"MARKET"}`

// laneStatusesOf 读出当前目标，返回查询给页面的结论和过期告警的结论。
func laneStatusesOf(t *testing.T, tradeStore *store.Store, executor *Executor) (map[string]LaneStatus, bool) {
	t.Helper()
	target, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	statuses, err := executor.LaneStatuses(context.Background(), target)
	require.NoError(t, err)
	different, err := executor.HasDifference(context.Background(), target)
	require.NoError(t, err)
	byID := make(map[string]LaneStatus, len(statuses))
	for _, status := range statuses {
		byID[status.InstrumentID] = status
	}
	return byID, different
}

// 页面看到的结论和执行器的决定一致：先用同一份持仓、目标问 LaneStatuses，再真的收敛一轮，
// “还会下单”的标的必须下了单，其余的一笔都不能下；过期告警（HasDifference）也是同一个结论。
func TestLaneStatusesMatchConvergeDecisions(t *testing.T) {
	const always = `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`
	const minimum50 = `{"order_mode":"MARKET","min_notional_swap":"50","rebalance_threshold_mode":"ALWAYS"}`
	for _, c := range []struct {
		name     string
		settings string
		// positions 是成员持仓（账户 → 数量），account-a 用币安代码、account-b 用 OKX 代码。
		positions map[string]string
		targets   []store.InstrumentTarget
		want      LaneState
	}{
		{"差额在默认阈值之内", legacyOrderSettings, map[string]string{"account-a": "95"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "100"}}, LaneInPlace},
		{"差额超过默认阈值", legacyOrderSettings, map[string]string{"account-a": "94"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "100"}}, LaneWillOrder},
		{"持仓等于目标", always, map[string]string{"account-a": "100"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "100"}}, LaneInPlace},
		{"保持当前持仓", always, map[string]string{"account-a": "7"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Hold: true}}, LaneInPlace},
		{"目标之外的持仓按 0 平掉", always, map[string]string{"account-a": "7"}, nil, LaneWillOrder},
		// 合计 100 等于目标，但 account-a 的空头和目标反向：按成员判断，要先平掉。
		{"成员持仓和目标反向", legacyOrderSettings, map[string]string{"account-a": "-10", "account-b": "110"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "100"}}, LaneWillOrder},
		// 价格 100：开 0.4 个 = 40，低于最小下单额 50。
		{"开仓差额低于最小下单额", minimum50, nil,
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.4"}}, LaneBelowMinNotional},
		{"开仓差额高于最小下单额", minimum50, nil,
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.6"}}, LaneWillOrder},
		// 合约减仓不受最小下单额限制。
		{"减仓不受最小下单额限制", minimum50, map[string]string{"account-a": "0.4"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0"}}, LaneWillOrder},
		// 残差 0.0004 不到一个步长（0.001），已有同向持仓：零头，当作已满足。
		{"残差不到一个步长", always, map[string]string{"account-a": "10"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "10.0004"}}, LaneInPlace},
		// 差额 0.0005 超过目标的 5%，但不到一个步长。
		{"默认阈值下不到一个步长的小差额", legacyOrderSettings, map[string]string{"account-a": "0.001"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.0015"}}, LaneInPlace},
		// 从空仓开新仓达不到交易所最小数量：不是零头，要人看。
		{"开新仓不到交易所最小数量", legacyOrderSettings, nil,
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.0004"}}, LaneBlocked},
		// 默认下单设置（合约最小下单额 5）：0.04 个 = 4 不下，0.06 个 = 6 下。
		{"默认设置下开仓低于最小下单额", defaultOrderSettings, nil,
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.04"}}, LaneBelowMinNotional},
		{"默认设置下开仓高于最小下单额", defaultOrderSettings, nil,
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "0.06"}}, LaneWillOrder},
		{"默认设置下不到一个步长的残差", defaultOrderSettings, map[string]string{"account-a": "10"},
			[]store.InstrumentTarget{{InstrumentID: swapID, Quantity: "10.0004"}}, LaneInPlace},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := swapPolicyFixture(t, c.settings)
			for accountID, quantity := range c.positions {
				symbol := "BTCUSDT"
				if accountID == "account-b" {
					symbol = swapID
				}
				f.position(t, accountID, symbol, quantity)
			}
			f.target(t, c.targets)

			statuses, different := laneStatusesOf(t, f.store, f.executor())
			require.Equal(t, c.want, statuses[swapID].State)
			require.Equal(t, c.want == LaneWillOrder || c.want == LaneBlocked, different, "过期告警和页面是同一个结论")

			f.converge(t)
			if c.want == LaneWillOrder {
				require.NotEmpty(t, f.orders.specs, "页面说还会下单，执行器就得下")
			} else {
				require.Empty(t, f.orders.specs, "页面说不会下单，执行器就不能下")
			}
		})
	}
}

func TestLaneStatusesReportBlockedLane(t *testing.T) {
	f := swapPolicyFixture(t, legacyOrderSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "DOGE-USDT-SWAP", Quantity: "10"}})

	statuses, different := laneStatusesOf(t, f.store, f.executor())

	require.Equal(t, LaneBlocked, statuses["DOGE-USDT-SWAP"].State)
	require.Equal(t, "没有启用的成员支持该标的", statuses["DOGE-USDT-SWAP"].Reason)
	require.True(t, different, "下不了单的差额仍算未到位")
}

// cachedOnlyPrices 记下取价时的上下文是不是“只读缓存”，并按 err 决定取不取得到。
type cachedOnlyPrices struct {
	err        error
	cachedOnly *[]bool
}

func (s cachedOnlyPrices) LatestPrice(ctx context.Context, _ string, _ exchange.SymbolKey) (Quote, error) {
	_, only := exchange.CachedQuotesOnly(ctx)
	*s.cachedOnly = append(*s.cachedOnly, only)
	return Quote{Price: shared.MustDecimal("100")}, s.err
}

// 查询只读报价缓存；判断要用的报价不在缓存里时给“未知”，不拿“还会下单”充数。
// 执行器自己（HasDifference）取不到报价时照旧按还有差额处理。
func TestLaneStatusesUnknownWhenQuoteNotCached(t *testing.T) {
	missing := exchange.ErrQuoteNotCached
	for _, c := range []struct {
		name      string
		settings  string
		position  string
		target    string
		priceErr  error
		want      LaneState
		different bool
	}{
		{"最小下单额要用报价", `{"order_mode":"MARKET","min_notional_swap":"50","rebalance_threshold_mode":"ALWAYS"}`,
			"", "0.4", missing, LaneUnknown, true},
		{"按持仓金额的阈值要用报价", `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"POSITION_RATIO","rebalance_threshold_ratio":"0.05"}`,
			"100", "104", missing, LaneUnknown, true},
		{"按目标数量的阈值不用报价", legacyOrderSettings, "95", "100", missing, LaneInPlace, false},
		{"交易所最小名义金额要用报价", legacyOrderSettings, "50", "100", missing, LaneUnknown, true},
		{"不到一个步长的残差不用报价", legacyOrderSettings, "0.001", "0.0015", missing, LaneInPlace, false},
		{"合约全平只查最小数量", `{"order_mode":"MARKET","min_notional_swap":"50","rebalance_threshold_mode":"ALWAYS"}`,
			"0.4", "0", missing, LaneWillOrder, true},
		{"报价在缓存里", `{"order_mode":"MARKET","min_notional_swap":"50","rebalance_threshold_mode":"ALWAYS"}`,
			"", "0.4", nil, LaneBelowMinNotional, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := swapPolicyFixture(t, c.settings)
			if c.position != "" {
				f.position(t, "account-a", "BTCUSDT", c.position)
			}
			f.target(t, []store.InstrumentTarget{{InstrumentID: swapID, Quantity: c.target}})
			target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			var cachedOnly []bool
			executor := f.executor()
			executor.Prices = cachedOnlyPrices{err: c.priceErr, cachedOnly: &cachedOnly}

			statuses, err := executor.LaneStatuses(context.Background(), target)
			require.NoError(t, err)
			require.Len(t, statuses, 1)
			require.Equal(t, c.want, statuses[0].State)
			for _, only := range cachedOnly {
				require.True(t, only, "查询取价必须带只读缓存的上下文")
			}

			cachedOnly = nil
			different, err := executor.HasDifference(context.Background(), target)
			require.NoError(t, err)
			require.Equal(t, c.different, different)
			for _, only := range cachedOnly {
				require.False(t, only, "执行器自己的判断照常取价")
			}
		})
	}
}

// 股票按市场规则顺延或视为已满足的几种情况，页面拿到的是具体原因。
func TestLaneStatusesStockMarketRules(t *testing.T) {
	const stock = "600519.XSHG"
	for _, c := range []struct {
		name   string
		setup  func(*testing.T, *stockFixture)
		target string
		state  LaneState
		reason string
	}{
		// 默认下单设置（现货最小下单额 10）：零股全卖按 50 股算金额，不能按步长取整成 0 判成“低于最小下单额”。
		{"默认设置下目标为 0 的零股会卖掉", func(t *testing.T, f *stockFixture) {
			f.setOrderSettings(t, defaultOrderSettings)
			f.holding(t, stock, "50", "50")
		}, "0", LaneWillOrder, ""},
		{"默认设置下不足一手", func(t *testing.T, f *stockFixture) { f.setOrderSettings(t, defaultOrderSettings) },
			"50", LaneBelowLot, ""},
		{"目标为 0 的零股会卖掉", func(t *testing.T, f *stockFixture) { f.holding(t, stock, "50", "50") },
			"0", LaneWillOrder, ""},
		{"停牌", func(_ *testing.T, f *stockFixture) {
			f.quotes[stock] = Quote{Price: shared.MustDecimal("10"), UpdatedAt: stockNow, Halted: true}
		}, "1000", LaneMarketDeferred, reasonHalted},
		{"涨停买不进", func(_ *testing.T, f *stockFixture) {
			f.quotes[stock] = Quote{
				Price: shared.MustDecimal("11"), UpdatedAt: stockNow,
				LimitUp: shared.MustDecimal("11"), LimitDown: shared.MustDecimal("9"), Bid: shared.MustDecimal("11"),
			}
		}, "1000", LaneMarketDeferred, reasonLimitUp},
		{"当日买入次日可卖", func(t *testing.T, f *stockFixture) { f.holding(t, stock, "300", "0") },
			"0", LaneMarketDeferred, reasonT1},
		{"不足一手", func(*testing.T, *stockFixture) {}, "50", LaneBelowLot, ""},
		{"够一手", func(*testing.T, *stockFixture) {}, "1000", LaneWillOrder, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newStockFixture(t)
			c.setup(t, f)
			f.target(t, store.InstrumentTarget{InstrumentID: stock, Quantity: c.target})

			statuses, different := laneStatusesOf(t, f.store, f.executor(t))

			require.Equal(t, c.state, statuses[stock].State)
			require.Equal(t, c.reason, statuses[stock].Reason)
			require.Equal(t, c.state == LaneWillOrder, different)
			f.converge(t)
			require.Equal(t, c.state == LaneWillOrder, len(f.orders.specs) > 0, "执行器的决定和页面一致")
		})
	}
}

// 股票的停牌、涨跌停都要看报价：报价不在缓存里时不下结论。
func TestLaneStatusesStockUnknownWithoutQuote(t *testing.T) {
	f := newStockFixture(t)
	f.target(t, store.InstrumentTarget{InstrumentID: "600519.XSHG", Quantity: "1000"})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	var cachedOnly []bool
	executor := f.executor(t)
	executor.Prices = cachedOnlyPrices{err: errors.New("行情不可用"), cachedOnly: &cachedOnly}

	statuses, err := executor.LaneStatuses(context.Background(), target)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, LaneUnknown, statuses[0].State)
}

func (f *stockFixture) setOrderSettings(t *testing.T, document string) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings("space-1", "logical-1", document)
	}))
}

// 金额不到交易所最小名义金额：已有同向持仓的零头当作已满足，开新仓要人看，合约全平不受限制。
func TestLaneStatusesExchangeMinimumNotional(t *testing.T) {
	const always = `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`
	for _, c := range []struct {
		name     string
		position string
		target   string
		want     LaneState
	}{
		// 价格 100，交易所最小名义金额 50：0.4 个 = 40。
		{"已有同向持仓的零头", "10", "10.4", LaneInPlace},
		{"开新仓不到最小名义金额", "", "0.4", LaneBlocked},
		{"合约全平不看最小名义金额", "0.4", "0", LaneWillOrder},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := swapPolicyFixture(t, always)
			f.setMinNotional(t, "BINANCE", "BTCUSDT", "50")
			f.setMinNotional(t, "OKX", swapID, "50")
			if c.position != "" {
				f.position(t, "account-a", "BTCUSDT", c.position)
			}
			f.target(t, []store.InstrumentTarget{{InstrumentID: swapID, Quantity: c.target}})

			statuses, different := laneStatusesOf(t, f.store, f.executor())
			require.Equal(t, c.want, statuses[swapID].State)
			require.Equal(t, c.want != LaneInPlace, different)

			f.converge(t)
			require.Equal(t, c.want == LaneWillOrder, len(f.orders.specs) > 0, "执行器的决定和页面一致")
		})
	}
}

// 两家交易所的最小值不同：首选成员（币安）金额不够、另一个成员（OKX）够时，执行器会换到 OKX 下单，
// 页面不能只看首选成员就说到位；两家都不够才是零头。
func TestLaneStatusesTryEveryCandidateMember(t *testing.T) {
	const always = `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`
	for _, c := range []struct {
		name        string
		okxMinimum  string
		want        LaneState
		wantAccount string
	}{
		// 价格 100，加仓 0.4 个 = 40：币安最小名义金额 100 不够，OKX 只看最小张数，够。
		{"另一家交易所能下", "", LaneWillOrder, "account-b"},
		{"两家都不够", "100", LaneInPlace, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := swapPolicyFixture(t, always)
			f.setMinNotional(t, "BINANCE", "BTCUSDT", "100")
			f.setMinNotional(t, "OKX", swapID, c.okxMinimum)
			f.position(t, "account-a", "BTCUSDT", "10")
			f.target(t, []store.InstrumentTarget{{InstrumentID: swapID, Quantity: "10.4"}})

			statuses, different := laneStatusesOf(t, f.store, f.executor())
			require.Equal(t, c.want, statuses[swapID].State)
			require.Equal(t, c.want == LaneWillOrder, different)

			f.converge(t)
			if c.wantAccount == "" {
				require.Empty(t, f.orders.specs)
				return
			}
			require.Len(t, f.orders.specs, 1)
			require.Equal(t, c.wantAccount, f.orders.specs[0].TradingAccountID, "执行器换到另一家交易所下单")
		})
	}
}
