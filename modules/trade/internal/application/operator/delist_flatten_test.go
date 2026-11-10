package operator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/okx"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func delistCommand(actionID, instrumentID string) FlattenCommand {
	command := flattenCommand(actionID)
	command.InstrumentID = instrumentID
	return command
}

func TestDelistLimitPriceRoundsAgainstTheOrderDirection(t *testing.T) {
	price := shared.MustDecimal("100.07")
	require.Equal(t, "97.0679", delistLimitPrice(price, exchange.SideSell, "").String(), "价格精度不明时不取整")
	require.Equal(t, "97", delistLimitPrice(price, exchange.SideSell, "0.1").String(), "卖出向下取整")
	require.Equal(t, "103.1", delistLimitPrice(price, exchange.SideBuy, "0.1").String(), "买入向上取整")
	require.Equal(t, "103", delistLimitPrice(shared.MustDecimal("100"), exchange.SideBuy, "1").String(), "正好整除不再加一档")
}

// 下架清仓用伪市价单：限价 + IOC，卖出价是参考价 ×（1 − 3%）。
func TestDelistFlattenPlacesPseudoMarketIOCOrders(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")

	_, err := fixture.service().FlattenLogicalAccount(context.Background(), delistCommand("delist-ioc", "BTC-USDT-SWAP"))

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	spec := fixture.orders.specs[0]
	require.Equal(t, exchange.OrderTypeLimit, spec.Type)
	require.Equal(t, exchange.FillPolicyIOC, spec.FillPolicy)
	require.Equal(t, "97", spec.LimitPrice.String())
	require.True(t, spec.ReducePositionOnly)
}

// 整组清仓和减仓仍然是市价单。
func TestWholeFlattenStillUsesMarketOrders(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), flattenCommand("flatten-market"))
	require.NoError(t, err)
	for _, spec := range fixture.orders.specs {
		require.Equal(t, exchange.OrderTypeMarket, spec.Type)
	}
}

// 清仓子单号固定 32 位（"mf" + 30 位十六进制），OKX 的 clOrdId 上限 32 位：尝试次数并进哈希，不拼后缀。
func TestFlattenChildClientOrderIDsFitEveryExchange(t *testing.T) {
	key := exchange.NewSymbolKey(exchange.MarketTypeSwap, "BTCUSDT")
	seen := map[string]struct{}{}
	for attempt := 0; attempt < 200; attempt++ {
		id := flattenClientOrderIDForSpec("delist-action-with-a-rather-long-identifier", "account-a", key, exchange.SideSell, shared.MustDecimal("3"), attempt)
		require.Len(t, id, 32)
		require.True(t, okx.ValidClientOrderID(id), id)
		seen[id] = struct{}{}
	}
	require.Len(t, seen, 200, "每次尝试的号都不同")
}

// 零成交的 IOC 单不算终结：同一个动作号再跑一轮，用新的单号按最新报价重新下。
func TestDelistFlattenRetriesAfterAnUnfilledIOCOrder(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-retry", "BTC-USDT-SWAP")
	fixture.orders.nextID = ""

	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	first := fixture.orders.specs[0].ClientOrderID
	// 这一单一股没成交就过期了（持仓没变）。（替身把交易所代码写成了内部标的 ID，这里改回真实的交易所代码，按标的查才找得到。）
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'EXPIRED', c_filled_quantity = '0', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-retry'`).Error)
	fixture.orders.nextID = "child-second"

	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 2, "再下一单，不是被判终结")
	second := fixture.orders.specs[1].ClientOrderID
	require.NotEqual(t, first, second)
	require.Len(t, second, 32, "第二笔的号也是 32 位，OKX 才收")
	require.True(t, okx.ValidClientOrderID(second))
}

// ageOrders 把本动作已有子单的创建时间往前拨：模拟它们是上一轮执行留下的。
func ageOrders(t *testing.T, fixture *operatorFixture, actionID string) {
	t.Helper()
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_ctime = datetime('now', '-10 minutes') WHERE c_owner_id = ?`, actionID).Error)
}

// currentRoundOrders 把本动作已有子单的创建时间往后拨几秒：订单创建时间只精确到秒，起点也取到秒，
// 同一秒内的子单算上一轮留下的；测试里要让它们算这一轮的。
func currentRoundOrders(t *testing.T, fixture *operatorFixture, actionID string) {
	t.Helper()
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_ctime = datetime('now', '+5 seconds') WHERE c_owner_id = ?`, actionID).Error)
}

// 被交易所拒绝：这一轮停下（PARTIAL）；同一个动作号重试会重新来过——起点之前的被拒子单不计，会再下单。
func TestDelistFlattenRetryAfterARejectionStartsOver(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-reject", "BTC-USDT-SWAP")
	fixture.orders.nextID = ""
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'REJECTED', c_reject_reason = '交易对暂停', c_submitted_at = 1000, c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-reject'`).Error)
	currentRoundOrders(t, fixture, "delist-reject")
	fixture.orders.specs = nil

	// 这一轮里最近一笔被拒：不再下，动作以 PARTIAL 结束。
	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Equal(t, string(operatordomain.StatusPartial), result.Action.Status)
	require.Contains(t, result.Accounts[0].Remaining[0].Reason, "被交易所拒绝")

	// 同一个动作号重试：重新进入运行，起点之后没有被拒的子单，会再下一笔。
	ageOrders(t, fixture, "delist-reject")
	fixture.orders.nextID = "child-retry"
	result, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "被拒一次不能让整轮平仓报废")
	require.Positive(t, result.AttemptStartedAt)
}

// 连续 20 笔 IOC 一股都没成交：这一轮封顶；部分成交打断计数；同号重试重新来过。
func TestDelistFlattenUnfilledStreakCapsThenResetsOnRetry(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-streak", "BTC-USDT-SWAP")
	fixture.orders.nextID = ""
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'EXPIRED', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-streak'`).Error)
	limit := "97"
	for i := 1; i < flattenMaxUnfilled; i++ {
		id := "old-" + string(rune('a'+i))
		fixture.order(t, store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: id, ClientOrderID: id,
			Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
			OrderType: "LIMIT", TimeInForce: "IOC", Side: "SELL", Quantity: "3", LimitPrice: &limit, ReferencePrice: "100",
			State: "EXPIRED", OwnerType: "OPERATOR", OwnerID: "delist-streak", LogicalAccountID: "logical-1", Version: 1,
		})
	}
	currentRoundOrders(t, fixture, "delist-streak")
	fixture.orders.specs = nil

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Contains(t, result.Accounts[0].Remaining[0].Reason, "需要人工处理")

	// 最近一笔有成交说明在推进：连续没成交的笔数从头数。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_filled_quantity = '1' WHERE c_order_id = 'old-t'`).Error)
	fixture.orders.nextID = "child-progress"
	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "部分成交不计入连续未成交，继续下单")

	// 封顶之后同号重试：这些子单都是上一轮留下的，不再计入。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_filled_quantity = '0', c_state = 'EXPIRED', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-streak'`).Error)
	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	fixture.orders.specs = nil
	ageOrders(t, fixture, "delist-streak")
	fixture.orders.nextID = "child-after-reset"
	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "同号重试重新来过，起点之前的子单不计")
}

// 两笔 IOC 之间至少隔一段时间：上一笔刚结束就先不下，不算终结。
func TestDelistFlattenPacesConsecutiveIOCOrders(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-pace", "BTC-USDT-SWAP")
	fixture.orders.nextID = ""
	service := fixture.service()
	service.DelistAttemptInterval = time.Minute
	_, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'EXPIRED', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-pace'`).Error)
	fixture.orders.nextID = "child-paced"

	result, err := service.FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "离上一笔结束太近，这一轮不再下")
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status, "不算终结，下一轮继续")
}

// 解除禁止就停：撤掉本动作的订单，记 COMPLETED 并带“已解除禁止”标记，不下单、不记 PARTIAL。
func TestDelistFlattenStopsWhenTheInstrumentIsUnblocked(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-unblock", "BTC-USDT-SWAP")
	fixture.orders.leaveOpen = map[string]bool{"child-account-a-BTC-USDT-SWAP": true}
	fixture.orders.nextID = ""
	first, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusRunning), first.Action.Status, "子单还挂着，动作继续")

	removed, err := fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	fixture.orders.specs, fixture.orders.leaveOpen, fixture.trace = nil, nil, nil
	wakes := 0
	service := fixture.service()
	service.Wake = func() { wakes++ }

	result, err := service.FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusCompleted), result.Action.Status)
	require.True(t, result.Unblocked)
	require.Equal(t, 1, wakes, "提前结束时唤醒目标，周期长的策略不用等下一份目标")
	require.Empty(t, fixture.orders.specs, "解除后不再下单")
	require.Contains(t, fixture.trace, "cancel:child-account-a-BTC-USDT-SWAP", "撤掉本动作自己的订单")
	var persisted struct {
		Unblocked bool `json:"unblocked"`
	}
	require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &persisted))
	require.True(t, persisted.Unblocked, "结果 JSON 顶层带 unblocked 标记，事件中心据此判断")
	// 完成之后再用同一个动作号调：直接返回旧结果。
	again, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.True(t, again.Unblocked)
}

// 混合交易所的组合：只平指定交易所的成员，另一家的持仓不动。
func TestDelistFlattenOnlyTouchesMembersOfTheGivenExchange(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	// account-b 是 OKX，同样持有 BTC 永续；它的交易所没有下架，不能被平。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_markets = 'SPOT,SWAP', c_margin_mode = 'CROSS' WHERE c_trading_account_id = 'account-b'`).Error)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		swap := fixture.instrument("account-b")
		swap.MarketType, swap.InstrumentID = "SWAP", "BTC-USDT-SWAP"
		swap.Linear, swap.ContractValue, swap.ContractValueAsset = true, "1", "BTC"
		if err := tx.UpsertInstrument(swap); err != nil {
			return err
		}
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-b", MarketType: "SWAP", ExchangeSymbol: swap.ExchangeSymbol, PositionSide: "NET",
			SignedQuantity: "4", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: fixture.now.UnixMilli(),
		})
	}))
	command := delistCommand("delist-binance", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL:3"}, placed(fixture))
	require.Len(t, result.Accounts, 1)
	require.Equal(t, "account-a", result.Accounts[0].TradingAccountID)

}

// 只平的标的恰好是套利底仓的对冲腿：不能静默当作平完，要以部分完成结束并写明原因。
func TestDelistFlattenDoesNotSilentlyCompleteForAnArbitrageHedgeLeg(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_logical_accounts SET c_fund_settings_json = '{"arbitrage_base_assets":["BTC"]}' WHERE c_logical_account_id = 'logical-1'`).Error)

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), delistCommand("delist-hedge", "BTC-USDT-SWAP"))

	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs, "对冲腿不自动平仓")
	require.Equal(t, string(operatordomain.StatusPartial), result.Action.Status)
	require.Len(t, result.Accounts[0].Remaining, 1)
	require.Equal(t, reasonArbitrageHedgeLeg, result.Accounts[0].Remaining[0].Reason)
}

// 下架清仓遇到整组清仓正在运行：返回“忙”；不同标的的下架清仓之间不互相冲突。
func TestDelistFlattenBusyWhenWholeFlattenIsRunning(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	_, _, err := fixture.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "whole", LogicalAccountID: "logical-1", ActionType: string(operatordomain.ActionFlatten),
		Reason: "清仓", RequestJSON: `{"logical_account_id":"logical-1"}`, Status: string(operatordomain.StatusRunning),
	})
	require.NoError(t, err)

	_, err = fixture.service().FlattenLogicalAccount(context.Background(), delistCommand("delist-busy", "BTC-USDT-SWAP"))

	require.ErrorIs(t, err, ErrLogicalAccountBusy)
	require.Empty(t, fixture.orders.specs)
}

// 重启恢复：工作线程用持久化的动作继续，下架清仓和减仓都认，交易所范围从请求里还原。
func TestResumeOperatorActionContinuesDelistFlattenAndReduce(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-resume", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"
	fixture.orders.leaveOpen = map[string]bool{"child-account-a-BTC-USDT-SWAP": true}
	fixture.orders.nextID = ""
	first, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusRunning), first.Action.Status)
	require.JSONEq(t, `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"BINANCE"}`, first.Action.RequestJSON)

	_, err = fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	fixture.orders.leaveOpen = nil
	require.NoError(t, fixture.service().ResumeOperatorAction(context.Background(), first.Action))
	action, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-resume")
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusCompleted), action.Status)

}

// 组合同时持有其他标的且有挂单时，下架清仓也能完成：清仓只看自己的标的，别的标的的挂单不撤、不算剩余。
func TestDelistFlattenCompletesWhileOtherInstrumentsHaveOpenOrders(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	other := activeOrder(fixture, "spot-open", "TARGET")
	other.MarketType, other.InstrumentID = "SPOT", "BTC-USDT-SPOT"
	fixture.order(t, other)
	// 平仓单发出后，持仓同步到位（永续持仓归零）。
	fixture.syncer.onSync = func(ctx context.Context, accountID string, _ int) error {
		for _, entry := range fixture.trace {
			if len(entry) > 6 && entry[:6] == "place:" {
				return fixture.store.Transaction(ctx, func(tx *store.Tx) error {
					return tx.UpsertPosition(store.PositionRecord{
						SpaceID: "space-1", TradingAccountID: "account-a", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
						SignedQuantity: "0", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: fixture.now.UnixMilli() + 1000,
					})
				})
			}
		}
		return nil
	}

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), delistCommand("delist-with-open", "BTC-USDT-SWAP"))

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusCompleted), result.Action.Status, "result=%+v", result.Accounts)
	for _, entry := range fixture.trace {
		require.NotContains(t, entry, "cancel:", "别的标的的挂单不撤")
	}
	logical, err := fixture.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
}

// 解除禁止提前结束时要确认撤单：本动作的子单还没结束就不能记 COMPLETED，也不能唤醒目标（执行器会把没结束的 OPERATOR 单当冲突、暂停组合）。
func TestDelistFlattenUnblockWaitsForCancelConfirmation(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-cancel-confirm", "BTC-USDT-SWAP")
	fixture.orders.leaveOpen = map[string]bool{"child-account-a-BTC-USDT-SWAP": true}
	fixture.orders.nextID = ""
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	_, err = fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	fixture.orders.specs = nil
	wakes := 0
	service := fixture.service()
	service.Wake = func() { wakes++ }

	// 撤单发出了但订单还开着（撤单没确认）：动作继续运行，不记完成、不唤醒。
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status)
	require.False(t, result.Unblocked)
	require.Zero(t, wakes)
	require.Empty(t, fixture.orders.specs, "解除后不再下单")
	require.Contains(t, fixture.trace, "cancel:child-account-a-BTC-USDT-SWAP")

	// 撤单确认之后才记完成并唤醒。
	fixture.orders.leaveOpen = nil
	result, err = service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusCompleted), result.Action.Status)
	require.True(t, result.Unblocked)
	require.Equal(t, 1, wakes)
}

// 同标的同交易所已经有另一个动作号在平：新号返回“忙”，不创建新动作、也不把旧动作当成新号的结果返回。
func TestDelistFlattenWithAnotherActionIDOnTheSameInstrumentIsBusy(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	_, _, err := fixture.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: "drill", LogicalAccountID: "logical-1", ActionType: string(operatordomain.ActionDelistFlatten), Reason: "演练",
		RequestJSON: `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"BINANCE"}`, Status: string(operatordomain.StatusRunning),
	})
	require.NoError(t, err)
	command := delistCommand("real", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.ErrorIs(t, err, ErrLogicalAccountBusy)
	require.Empty(t, result.Action.ActionID, "不返回别人的动作")
	require.Empty(t, fixture.orders.specs)
	_, getErr := fixture.store.GetOperatorAction(context.Background(), "space-1", "real")
	require.Error(t, getErr, "新号没有创建")
}

// IOC 之间的间隔里不能空转同步：间隔没到时直接睡到间隔结束再往下做。否则轮内循环每 200 毫秒一轮、每轮两次轻量同步，
// 对币安现货加永续要发 3 个接口，一次调用的预算里就是几十次同步，会打满 IP 限频。
func TestDelistFlattenWaitsOutTheIntervalWithoutSpinningSyncs(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	service := fixture.service()
	service.FlattenMaxAttempts = 1000
	service.FlattenRetryInterval = 10 * time.Millisecond
	service.FlattenTimeout = 4 * time.Second
	service.DelistAttemptInterval = 400 * time.Millisecond
	// 只处理币安成员：预算不被两个成员分掉，到期时也不会落在某个数据库调用里。
	command := delistCommand("delist-spin", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"

	_, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)

	// 第一笔和第二笔下单之间的同步次数：等间隔的 400 毫秒里不能同步（空转的话每 10 毫秒一轮、两次同步，上百次）。
	first, second := -1, -1
	for index, entry := range fixture.trace {
		if len(entry) > 6 && entry[:6] == "place:" {
			if first < 0 {
				first = index
			} else if second < 0 {
				second = index
			}
		}
	}
	require.GreaterOrEqual(t, second, 0, "间隔过后继续下第二笔，trace=%v", fixture.trace)
	syncs := 0
	for _, entry := range fixture.trace[first:second] {
		if entry == "sync:account-a" {
			syncs++
		}
	}
	require.LessOrEqual(t, syncs, 6, "两笔之间同步了 %d 次", syncs)
}

// 清仓这一族的 30 分钟期限从这一轮的起点算：动作创建很久之后用同一个动作号重试，不能一次调用就被记 PARTIAL。
func TestDelistFlattenRetryDeadlineStartsAtTheRoundNotTheActionCreation(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-old", "BTC-USDT-SWAP")
	fixture.orders.leaveOpen = map[string]bool{"child-account-a-BTC-USDT-SWAP": true}
	fixture.orders.nextID = ""
	service := fixture.service()
	service.Now = time.Now
	first, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusRunning), first.Action.Status)

	// 动作是 40 分钟前创建的，且已经结束（FAILED）；同一个动作号重试。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_ctime = datetime('now', '-40 minutes'), c_status = 'FAILED' WHERE c_action_id = 'delist-old'`).Error)
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status, "期限从这一轮的起点算，不是动作创建时间")

	// 这一轮自己超过 30 分钟才记 PARTIAL。
	var persisted struct {
		AttemptStartedAt int64 `json:"attempt_started_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &persisted))
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_result_json = json_set(c_result_json, '$.attempt_started_at', ?)`, time.Now().Add(-40*time.Minute).UnixMilli()).Error)
	result, err = service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.StatusPartial), result.Action.Status)
}

// 等间隔的睡眠期间解除禁止：睡醒后先查名单，不能再下一笔 IOC（多卖的一笔随后又会被执行器买回来）。
func TestDelistFlattenDoesNotPlaceAfterTheUnblockDuringTheIntervalSleep(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	service := fixture.service()
	service.FlattenMaxAttempts = 1000
	service.FlattenRetryInterval = 10 * time.Millisecond
	service.DelistAttemptInterval = 800 * time.Millisecond
	command := delistCommand("delist-sleep-unblock", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"
	go func() {
		time.Sleep(300 * time.Millisecond) // 第一笔之后、正在睡
		_, _ = fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	}()

	result, err := service.FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "解除禁止之后不再下单")
	require.Equal(t, string(operatordomain.StatusCompleted), result.Action.Status)
	require.True(t, result.Unblocked)
}

// 睡醒后离本账户期限不够再发一单（轮间等待加发单余量）：不睡了，这一轮干净地结束，不空转到期限、不写错误。
func TestDelistFlattenEndsTheRoundWhenTheWakeUpLeavesNoRoomToPlace(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	service := fixture.service()
	service.FlattenMaxAttempts = 1000
	service.FlattenRetryInterval = 10 * time.Millisecond
	service.FlattenTimeout = 1500 * time.Millisecond
	service.DelistAttemptInterval = 800 * time.Millisecond
	command := delistCommand("delist-no-room", "BTC-USDT-SWAP")
	command.Exchange = "BINANCE"

	started := time.Now()
	result, err := service.FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Less(t, time.Since(started), 700*time.Millisecond, "没有睡到间隔结束，也没有空转到期限")
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, string(operatordomain.StatusRunning), result.Action.Status, "下一次调用接着来")
	require.Empty(t, result.Action.LastError)
}
