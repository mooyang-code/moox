package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// swapPolicyFixture 建一个只开合约的两成员组合，价格 100；quantity 都是基础币数量。
func swapPolicyFixture(t *testing.T, settings string) *targetFixture {
	t.Helper()
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	f.setOrderSettings(t, settings)
	return f
}

func (f *targetFixture) converge(t *testing.T) Result {
	t.Helper()
	executor := f.executor()
	executor.MinBatchInterval = 0
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	return result
}

func TestThresholdDefaultKeepsTargetRatioBehaviour(t *testing.T) {
	// 目标 100、持仓 95：差额 5 恰好等于 100 × 5%，不调；持仓 94 差额 6，要调。
	for _, c := range []struct {
		position string
		orders   int
	}{{"95", 0}, {"94", 1}, {"100", 0}} {
		f := swapPolicyFixture(t, legacyOrderSettings)
		f.position(t, "account-a", "BTCUSDT", c.position)
		f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
		f.converge(t)
		require.Len(t, f.orders.specs, c.orders, "持仓 %s", c.position)
	}
}

func TestThresholdPositionRatioUsesHoldingNotional(t *testing.T) {
	settings := `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"POSITION_RATIO","rebalance_threshold_ratio":"0.05"}`
	// 持仓 100（金额 10000），阈值 500；调仓 4 个（400）不调，5 个（500，等于阈值）要调，6 个要调。
	for _, c := range []struct {
		target string
		orders int
	}{{"104", 0}, {"105", 1}, {"106", 1}} {
		f := swapPolicyFixture(t, settings)
		f.position(t, "account-a", "BTCUSDT", "100")
		f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: c.target}})
		f.converge(t)
		require.Len(t, f.orders.specs, c.orders, "目标 %s", c.target)
	}
}

func TestThresholdEquityRatioUsesLatestEquityPoint(t *testing.T) {
	settings := `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"EQUITY_RATIO","rebalance_threshold_ratio":"0.01"}`
	for _, c := range []struct {
		name   string
		equity string
		orders int
	}{
		// 调仓 3 个，金额 300；权益 100000 的 1% 是 1000：不调。
		{"权益大，阈值高", "100000", 0},
		// 权益 20000 的 1% 是 200：调。
		{"权益小，阈值低", "20000", 1},
		// 金额恰好等于阈值：调。
		{"等于阈值", "30000", 1},
		// 没有权益采样点：阈值不生效，宁可多调。
		{"取不到权益", "", 1},
	} {
		f := swapPolicyFixture(t, settings)
		if c.equity != "" {
			require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
				return tx.UpsertLogicalAccountEquityPoint(store.EquityPointRecord{
					SpaceID: "space-1", LogicalAccountID: "logical-1", BucketTime: 1_000, SourceTime: 1_000,
					Equity: c.equity, AvailableFunds: c.equity, UsedMargin: "0",
				})
			}))
		}
		f.position(t, "account-a", "BTCUSDT", "100")
		f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "103"}})
		f.converge(t)
		require.Len(t, f.orders.specs, c.orders, c.name)
	}
}

func TestThresholdAlwaysTradesTinyDifferences(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS"}`)
	f.position(t, "account-a", "BTCUSDT", "1000")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1001"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
}

// 建仓（持仓为 0）和清仓（目标为 0）在任何模式下都不受阈值限制，即使比例设成 100%。
func TestThresholdNeverBlocksOpeningOrClosing(t *testing.T) {
	for _, mode := range []string{"TARGET_RATIO", "POSITION_RATIO", "EQUITY_RATIO"} {
		settings := `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"` + mode + `","rebalance_threshold_ratio":"1"}`
		opening := swapPolicyFixture(t, settings)
		opening.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
		opening.converge(t)
		require.Len(t, opening.orders.specs, 1, "%s 建仓", mode)

		closing := swapPolicyFixture(t, settings)
		closing.position(t, "account-a", "BTCUSDT", "10")
		closing.target(t, nil)
		closing.converge(t)
		require.Len(t, closing.orders.specs, 1, "%s 清仓", mode)
	}
}

// 有成员的持仓和目标反向时不跳过，哪怕合计持仓看起来离目标很近。
func TestThresholdDoesNotSkipWhenAMemberHoldsTheOppositeSide(t *testing.T) {
	f := swapPolicyFixture(t, legacyOrderSettings)
	f.position(t, "account-a", "BTCUSDT", "-50")
	f.position(t, "account-b", "BTC-USDT-SWAP", "150")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1, "合计 100 等于目标，但 account-a 的空头要先平掉")
	require.Equal(t, "account-a", f.orders.specs[0].TradingAccountID)
	require.Equal(t, exchange.SideBuy, f.orders.specs[0].Side)
}

// 过期告警（HasDifference）和收敛用同一份阈值判断。
func TestHasDifferenceAgreesWithConvergeOnThreshold(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"POSITION_RATIO","rebalance_threshold_ratio":"0.05"}`)
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "104"}})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	different, err := f.executor().HasDifference(context.Background(), target)
	require.NoError(t, err)
	require.False(t, different)
}

// 设置读不出来时这个组合不下单，原因写进目标。
func TestUnreadableOrderSettingsPauseTheTarget(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"LIMIT"}`)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	result := f.converge(t)
	require.Equal(t, StatusPaused, result.Status)
	require.Empty(t, f.orders.specs)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Contains(t, target.LastError, "下单设置")
}

func TestConfiguredMinimumNotionalSkipsSmallOpening(t *testing.T) {
	// 配置合约最小下单额 50；价格 100：0.4 个 = 40 不下，0.6 个 = 60 下。
	settings := `{"order_mode":"MARKET","min_notional_swap":"50"}`
	small := swapPolicyFixture(t, settings)
	small.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.4"}})
	result := small.converge(t)
	require.Empty(t, small.orders.specs)
	require.Equal(t, StatusBlocked, result.Status, "从空仓开新仓达不到最小下单额需要人看")

	large := swapPolicyFixture(t, settings)
	large.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.6"}})
	large.converge(t)
	require.Len(t, large.orders.specs, 1)
}

// 合约减仓、清仓低于配置的最小下单额也能下，否则剩余仓位永远平不掉。
func TestConfiguredMinimumNotionalDoesNotBlockSwapReduceOrClose(t *testing.T) {
	settings := `{"order_mode":"MARKET","min_notional_swap":"50"}`
	closing := swapPolicyFixture(t, settings)
	closing.position(t, "account-a", "BTCUSDT", "0.3")
	closing.target(t, nil)
	closing.converge(t)
	require.Len(t, closing.orders.specs, 1, "清仓 0.3 个 = 30，低于 50 也要下")

	reducing := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"50","rebalance_threshold_mode":"ALWAYS"}`)
	reducing.position(t, "account-a", "BTCUSDT", "1")
	reducing.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.8"}})
	reducing.converge(t)
	require.Len(t, reducing.orders.specs, 1, "减仓 0.2 个 = 20，低于 50 也要下")
	require.Equal(t, exchange.SideSell, reducing.orders.specs[0].Side)
}

// 现货按现货的最小下单额，合约按合约的：现货默认 10。
func TestConfiguredMinimumNotionalIsPerMarket(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"150","min_notional_swap":"0"}`)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"}, // 100，低于现货 150
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}, // 100，合约不限
	})
	f.now = time.Now().UTC()
	f.converge(t)
	require.Equal(t, []string{"BTC-USDT-SWAP:BUY"}, placedInstruments(f))
}

// 数量取整后不到交易所的最小数量（OKX 的 minSz）时下不了单：换成金额就是 minSz × 价格。
func TestExchangeMinimumQuantityActsAsMinimumNotional(t *testing.T) {
	f := swapPolicyFixture(t, legacyOrderSettings)
	f.setQuantityRule(t, "BINANCE", "BTCUSDT", "1", "10") // 合约面值 0.001，最小 10 张 = 0.01 个 = 金额 1
	f.setQuantityRule(t, "OKX", "BTC-USDT-SWAP", "1", "10")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "0.005"}})
	f.converge(t)
	require.Empty(t, f.orders.specs)
}

func TestSettingsDefaultsAreWhatTheExecutorUses(t *testing.T) {
	settings, err := loadOrderSettings(store.LogicalAccountRecord{OrderSettingsJSON: "{}"})
	require.NoError(t, err)
	require.Equal(t, ordersettings.ThresholdTargetRatio, settings.ThresholdMode)
	require.Equal(t, shared.MustDecimal("100000").String(), settings.MaxChildNotional.String())
}

func mustSettings(t *testing.T, raw string) ordersettings.Settings {
	t.Helper()
	settings, err := ordersettings.Parse(raw)
	require.NoError(t, err)
	return settings
}

func mustDecimal(raw string) shared.Decimal { return shared.MustDecimal(raw) }
