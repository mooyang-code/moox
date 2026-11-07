package operator

import (
	"context"
	"encoding/json"
	"testing"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
	// 这一单一股没成交就过期了（持仓没变）。
	// （替身把交易所代码写成了内部标的 ID，这里改回真实的交易所代码，按标的查才找得到。）
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'EXPIRED', c_filled_quantity = '0', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-retry'`).Error)
	fixture.orders.nextID = "child-second"

	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 2, "再下一单，不是被判终结")
	require.NotEqual(t, first, fixture.orders.specs[1].ClientOrderID)
	require.Equal(t, first+"a1", fixture.orders.specs[1].ClientOrderID)
}

// 被交易所拒绝的不再重试；下满次数仍未成交的记终结原因。
func TestDelistFlattenStopsAfterRejectionOrTooManyAttempts(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-stop", "BTC-USDT-SWAP")
	fixture.orders.nextID = ""
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'REJECTED', c_reject_reason = '交易对暂停', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-stop'`).Error)
	fixture.orders.specs = nil

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Contains(t, result.Accounts[0].Remaining[0].Reason, "被交易所拒绝")

	// 下满次数：把已结束的子单数补到上限。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'EXPIRED' WHERE c_owner_id = 'delist-stop'`).Error)
	limit := "97"
	for i := 1; i < delistMaxAttempts; i++ {
		fixture.order(t, store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "old-" + string(rune('a'+i)), ClientOrderID: "old-" + string(rune('a'+i)),
			Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
			OrderType: "LIMIT", TimeInForce: "IOC", Side: "SELL", Quantity: "3", LimitPrice: &limit, ReferencePrice: "100",
			State: "EXPIRED", OwnerType: "OPERATOR", OwnerID: "delist-stop", LogicalAccountID: "logical-1", Version: 1,
		})
	}
	result, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Contains(t, result.Accounts[0].Remaining[0].Reason, "需要人工处理")

	// 最近一笔有成交说明在推进：连续没成交的笔数从头数，不会因为总笔数多就终结。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_filled_quantity = '1' WHERE c_order_id = 'old-t'`).Error)
	fixture.orders.nextID = "child-progress"
	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "部分成交不计入连续未成交，继续下单")
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
	require.Equal(t, "RUNNING", first.Action.Status, "子单还挂着，动作继续")

	removed, err := fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	require.True(t, removed)
	fixture.orders.specs, fixture.orders.leaveOpen, fixture.trace = nil, nil, nil
	wakes := 0
	service := fixture.service()
	service.Wake = func() { wakes++ }

	result, err := service.FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
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
		SpaceID: "space-1", ActionID: "whole", LogicalAccountID: "logical-1", ActionType: "FLATTEN",
		Reason: "清仓", RequestJSON: `{"logical_account_id":"logical-1"}`, Status: "RUNNING",
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
	require.Equal(t, "RUNNING", first.Action.Status)
	require.JSONEq(t, `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"BINANCE"}`, first.Action.RequestJSON)

	_, err = fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	fixture.orders.leaveOpen = nil
	require.NoError(t, fixture.service().ResumeOperatorAction(context.Background(), first.Action))
	action, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-resume")
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", action.Status)

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
	require.Equal(t, "COMPLETED", result.Action.Status, "result=%+v", result.Accounts)
	for _, entry := range fixture.trace {
		require.NotContains(t, entry, "cancel:", "别的标的的挂单不撤")
	}
	logical, err := fixture.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
}
