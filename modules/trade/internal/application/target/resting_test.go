package target

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// bookPriceStub 是带买一卖一的报价替身。
type bookPriceStub struct {
	targetPriceStub
	bid, ask shared.Decimal
}

func (s bookPriceStub) Book(context.Context, string, exchange.SymbolKey) (shared.Decimal, shared.Decimal, error) {
	return s.bid, s.ask, nil
}

func newBookPrices() bookPriceStub {
	return bookPriceStub{
		targetPriceStub: targetPriceStub{price: shared.MustDecimal("100")},
		bid:             shared.MustDecimal("99.9"), ask: shared.MustDecimal("100.1"),
	}
}

const restingSettings = `{"order_mode":"MARKET","min_notional_swap":"0","resting_ratio":"1"}`

// restingExecutor 用带买一卖一的报价；一轮之内不受批间隔限制。
func (f *targetFixture) restingExecutor() *Executor {
	executor := f.executor()
	executor.MinBatchInterval = 0
	executor.Prices = newBookPrices()
	return executor
}

func (f *targetFixture) restingOrder(t *testing.T, orderID, side, quantity, filled string, submittedAt time.Time) {
	t.Helper()
	state := "OPEN"
	if filled != "0" {
		state = "PARTIALLY_FILLED"
	}
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: orderID, Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: side, Quantity: quantity, LimitPrice: strPtr("99.9"), ReferencePrice: "100",
		FilledQuantity: filled, OwnerType: "TARGET", OwnerID: "target-current", State: state, SubmittedAt: submittedAt.UnixMilli(), Version: 1,
	})
}

// 加仓的一片按预算挑中后改成挂单：限价 + GTC，买单挂买一价。
func TestRestingBuyIsLimitGTCAtBestBid(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	spec := f.orders.specs[0]
	require.Equal(t, exchange.OrderTypeLimit, spec.Type)
	require.Equal(t, exchange.FillPolicyGTC, spec.FillPolicy)
	require.Equal(t, "99.9", spec.LimitPrice.String())
	require.Equal(t, "10", spec.Quantity.String())
}

func TestRestingSellIsLimitGTCAtBestAsk(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.position(t, "account-a", "BTCUSDT", "120")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)
	require.Equal(t, "100.1", f.orders.specs[0].LimitPrice.String())
}

// 建仓、清仓不挂单，没有买一卖一的报价来源时退回普通下单。
func TestRestingIsSkippedForOpeningClosingAndWithoutABook(t *testing.T) {
	opening := swapPolicyFixture(t, restingSettings)
	opening.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	_, err := opening.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderTypeMarket, opening.orders.specs[0].Type, "建仓不挂单")

	closing := swapPolicyFixture(t, restingSettings)
	closing.position(t, "account-a", "BTCUSDT", "10")
	closing.target(t, nil)
	_, err = closing.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderTypeMarket, closing.orders.specs[0].Type, "清仓不挂单")

	noBook := swapPolicyFixture(t, restingSettings)
	noBook.position(t, "account-a", "BTCUSDT", "100")
	noBook.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	executor := noBook.executor()
	executor.MinBatchInterval = 0
	_, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderTypeMarket, noBook.orders.specs[0].Type, "取不到买一卖一，退回普通下单")
}

func TestRestingRatioZeroNeverRests(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0"}`)
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, exchange.OrderTypeMarket, f.orders.specs[0].Type)
}

// 挂单的剩余量当作已在路上：整笔差额都有挂单覆盖时不再下单，目标定为已收敛，唤醒时间取到有效期末。
func TestRestingOrderCoveringTheWholeDifferenceStopsFurtherOrders(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, f.orders.specs, "挂单覆盖了整笔差额，不再追加吃单")
	require.Empty(t, f.orders.canceled)
	require.Equal(t, StatusConverged, result.Status)
	require.Greater(t, result.RetryAfter, 30*time.Minute)
}

// 挂单部分成交：成交的部分已在持仓里，剩下的挂单量仍然覆盖剩下的差额，不超买。
func TestPartiallyFilledRestingOrderDoesNotCauseOverbuy(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "104")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.restingOrder(t, "resting-1", "BUY", "10", "4", f.now)
	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, f.orders.specs, "剩余挂单 6 正好等于剩余差额 6")
	require.Empty(t, f.orders.canceled)
}

// 挂单只覆盖一部分差额：差额扣掉挂单剩余量后，其余部分照常下单（不再挂单）。
func TestRestingOrderCoveringPartOfTheDifferenceLeavesTheRestToTheTaker(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.restingOrder(t, "resting-1", "BUY", "4", "0", f.now)
	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "6", f.orders.specs[0].Quantity.String(), "差额 10 − 挂单剩余 4")
	require.Equal(t, exchange.OrderTypeMarket, f.orders.specs[0].Type, "这个标的已经有挂单，不再挂")
}

// 标的已经到位（挂单之外的成交补上了）或差额反向时，挂着的单会买多或卖多：撤掉。
func TestStaleRestingOrdersAreCancelled(t *testing.T) {
	for name, position := range map[string]string{"已到位": "110", "反向": "120", "挂单超出差额": "108"} {
		f := swapPolicyFixture(t, restingSettings)
		f.now = time.Now().UTC()
		f.position(t, "account-a", "BTCUSDT", position)
		f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
		f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
		result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err, name)
		require.Equal(t, []string{"resting-1"}, f.orders.canceled, name)
		require.Equal(t, StatusConverging, result.Status, name)
		require.Empty(t, f.orders.specs, name)
	}
}

// 超过挂单寿命的挂单撤掉；没到期时唤醒时间取到期时刻。
func TestRestingTTLCancelsExpiredOrdersAndWakesAtExpiry(t *testing.T) {
	settings := `{"order_mode":"MARKET","min_notional_swap":"0","resting_ratio":"1","resting_ttl_seconds":600}`
	expired := swapPolicyFixture(t, settings)
	expired.now = time.Now().UTC()
	expired.position(t, "account-a", "BTCUSDT", "100")
	expired.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	expired.restingOrder(t, "resting-1", "BUY", "10", "0", expired.now.Add(-700*time.Second))
	_, err := expired.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, []string{"resting-1"}, expired.orders.canceled)

	fresh := swapPolicyFixture(t, settings)
	fresh.now = time.Now().UTC()
	fresh.position(t, "account-a", "BTCUSDT", "100")
	fresh.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	fresh.restingOrder(t, "resting-1", "BUY", "10", "0", fresh.now.Add(-100*time.Second))
	result, err := fresh.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, fresh.orders.canceled)
	require.Equal(t, StatusConverged, result.Status)
	require.InDelta(t, float64(500*time.Second), float64(result.RetryAfter), float64(2*time.Second), "到期时刻早于有效期末")
}

// 人工单（OPERATOR）不是执行器的：挂单逻辑不会撤它，组合按冲突规则暂停。
func TestRestingLogicNeverCancelsOperatorOrders(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "110")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "manual-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "manual-1", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "10", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: "action-1", State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Empty(t, f.orders.canceled)
}

// 挑选：每个方向单独按预算挑，同一个种子结果相同。
func TestPickRestingIsReproducibleAndFollowsPerDirectionBudget(t *testing.T) {
	var actions []laneAction
	desired := make(map[string]desiredTarget)
	positions := make(map[string]shared.Decimal)
	add := func(symbol string, delta string) {
		id := symbol + "-USDT-SWAP"
		desired[id] = desiredTarget{Quantity: shared.MustDecimal("200")}
		positions[id] = shared.MustDecimal("100")
		actions = append(actions, laneAction{
			instrument: store.InstrumentRecord{InstrumentID: id, MarketType: "SWAP"},
			delta:      shared.MustDecimal(delta),
		})
	}
	for _, symbol := range []string{"A", "B", "C", "D"} {
		add(symbol, "10")
	}
	for _, symbol := range []string{"E", "F"} {
		add(symbol, "-10")
	}
	members := []memberState{{positions: positions}}
	settings, err := ordersettings.Parse(`{"resting_ratio":"0.5"}`)
	require.NoError(t, err)
	env := &laneEnv{settings: settings}
	notional := func(action laneAction) (shared.Decimal, bool) {
		return action.delta.Abs().Mul(shared.MustDecimal("100")), true
	}
	target := store.LogicalAccountTargetRecord{LogicalAccountID: "logical-1", TargetID: "target-1"}

	picked := pickResting(env, target, actions, desired, members, notional)
	require.Equal(t, picked, pickResting(env, target, actions, desired, members, notional), "同一个种子结果相同")
	buys, sells := 0, 0
	for _, action := range actions {
		if _, ok := picked[action.instrument.InstrumentID]; ok {
			if action.delta.IsNegative() {
				sells++
			} else {
				buys++
			}
		}
	}
	require.Equal(t, 2, buys, "买方向 4 个标的，预算一半，挑 2 个")
	require.Equal(t, 1, sells, "卖方向 2 个标的，预算一半，挑 1 个")

	other := store.LogicalAccountTargetRecord{LogicalAccountID: "logical-1", TargetID: "target-2"}
	different := false
	for index := 0; index < 20 && !different; index++ {
		other.TargetID = fmt.Sprintf("target-%d", index)
		different = fmt.Sprint(pickResting(env, other, actions, desired, members, notional)) != fmt.Sprint(picked)
	}
	require.True(t, different, "换一个目标挑选的标的不同")
}

func TestRestingEligibleExcludesOpeningClosingAndReversal(t *testing.T) {
	instrument := store.InstrumentRecord{InstrumentID: "BTC-USDT-SWAP"}
	action := laneAction{instrument: instrument, delta: shared.MustDecimal("5")}
	members := func(position string) []memberState {
		return []memberState{{positions: map[string]shared.Decimal{"BTC-USDT-SWAP": shared.MustDecimal(position)}}}
	}
	target := func(quantity string) map[string]desiredTarget {
		return map[string]desiredTarget{"BTC-USDT-SWAP": {Quantity: shared.MustDecimal(quantity)}}
	}
	require.True(t, restingEligible(action, target("10"), members("5")), "加仓")
	require.False(t, restingEligible(action, target("10"), members("0")), "建仓")
	require.False(t, restingEligible(action, target("0"), members("5")), "清仓")
	require.False(t, restingEligible(action, target("10"), members("-5")), "反向")
	action.hasResting = true
	require.False(t, restingEligible(action, target("10"), members("5")), "已有挂单")
}

// 系统单（BNB 补买、稳定币调平）不是外部单也不是人工单：不会暂停组合，也不会被当成本目标的子单。
func TestSystemOrdersDoNotPauseTheLogicalAccountOrBlockTheTarget(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "system-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "system-1", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "SYSTEM", OwnerID: "bnb-reserve", State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "place", result.Action)
	require.Len(t, f.orders.specs, 1, "系统单不影响目标下单")
	require.Empty(t, f.orders.canceled, "也不会被当成本目标的挂单撤掉")
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
}

// 挂单取不到买一卖一时退回伪市价，仍受最多尝试次数限制：已经有 4 笔零成交 IOC 就记未到位，不再下第 5 笔。
func TestRestingFallbackToPseudoMarketStillHonoursMaxAttempts(t *testing.T) {
	f := swapPolicyFixture(t, `{"min_notional_swap":"0","resting_ratio":"1"}`)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	for attempt := 0; attempt < 4; attempt++ {
		f.iocChild(t, "ioc-a"+string(rune('a'+attempt)), "10", "0", attempt)
		f.iocChildOn(t, "account-b", "OKX", "BTC-USDT-SWAP", "ioc-b"+string(rune('a'+attempt)), "10", "0", attempt)
	}
	executor := f.executor() // 没有买一卖一的报价来源：挂单取不到价格
	executor.MinBatchInterval = 0
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, f.orders.specs, "次数用满，不再下第 5 笔")
	require.Equal(t, StatusBlocked, result.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Contains(t, target.BlockedTargets[0].Reason, "未到位")
	require.Len(t, executor.warned, 1)
}

// 取不到买一卖一、次数没用完时，退回的伪市价单按已有次数继续往上加滑点。
func TestRestingFallbackContinuesTheAttemptCount(t *testing.T) {
	f := swapPolicyFixture(t, `{"min_notional_swap":"0","resting_ratio":"1"}`)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.iocChild(t, "ioc-0", "10", "0", 0)
	f.iocChildOn(t, "account-b", "OKX", "BTC-USDT-SWAP", "ioc-b0", "10", "0", 0)
	executor := f.executor()
	executor.MinBatchInterval = 0
	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, 2, f.orders.specs[0].Attempt, "两个成员各一笔零成交 IOC，按标的数是第 2 次")
	require.Equal(t, exchange.FillPolicyIOC, f.orders.specs[0].FillPolicy)
}

// 组合上挂着系统单时，收敛的快路径（只在等本目标子单时不加载成员、不逐个标的计算）照常生效。
func TestSystemOrdersDoNotDefeatTheWaitingFastPath(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "child-open", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "child-open", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: "5", ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: target.TargetID, State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	waiting, err := f.executor().waitingOnChildren(context.Background(), "space-1", "logical-1", target)
	require.NoError(t, err)
	require.True(t, waiting)
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "system-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: "system-1", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("90"), ReferencePrice: "100",
		OwnerType: "SYSTEM", OwnerID: "stable-rebalance", State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	waiting, err = f.executor().waitingOnChildren(context.Background(), "space-1", "logical-1", target)
	require.NoError(t, err)
	require.True(t, waiting, "挂着系统单，快路径照常")
}
