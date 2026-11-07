package operator

import (
	"context"
	"sort"
	"testing"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// newMixedFlattenFixture 把 account-a 改成同时开通现货和合约：现货钱包里有 2 个 BTC（MooX 买过，受管），
// 合约有 3 个 BTC 多头；两个标的的交易所代码同名（BTCUSDT）。
func newMixedFlattenFixture(t *testing.T) *operatorFixture {
	t.Helper()
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.orders.nextID = ""
	ctx := context.Background()
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trading_accounts SET c_markets = 'SPOT,SWAP', c_margin_mode = 'CROSS' WHERE c_trading_account_id = 'account-a'`).Error)
	require.NoError(t, fixture.store.Transaction(ctx, func(tx *store.Tx) error {
		swap := fixture.instrument("account-a")
		swap.MarketType, swap.InstrumentID = "SWAP", "BTC-USDT-SWAP"
		swap.Linear, swap.ContractValue, swap.ContractValueAsset = true, "1", "BTC"
		if err := tx.UpsertInstrument(swap); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "bought", ClientOrderID: "bought",
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
			OrderType: "MARKET", Side: "BUY", Quantity: "2", FilledQuantity: "2", ReferencePrice: "100",
			State: "FILLED", OwnerType: "OPERATOR", OwnerID: "action-0", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
	account, err := fixture.store.GetTradingAccountByID(ctx, "account-a")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Wallet: "UNIFIED", Asset: "USDT", Available: "1000", Total: "1000"},
		{Wallet: "UNIFIED", Asset: "BTC", Available: "2", Total: "2"},
	}
	require.NoError(t, fixture.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: fixture.now.UnixMilli(), LastSyncAt: fixture.now.UnixMilli(),
		})
	}))
	require.NoError(t, fixture.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "3", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: fixture.now.UnixMilli(),
		})
	}))
	return fixture
}

func placed(fixture *operatorFixture) []string {
	var result []string
	for _, spec := range fixture.orders.specs {
		result = append(result, spec.InstrumentID+":"+string(spec.Side)+":"+spec.Quantity.String())
	}
	sort.Strings(result)
	return result
}

func flattenCommand(actionID string) FlattenCommand {
	return FlattenCommand{SpaceID: "space-1", ActionID: actionID, LogicalAccountID: "logical-1", Reason: "close risk"}
}

// 混合持仓的组合一键清仓：同名的现货和合约都清掉，现货卖出，合约 reduceOnly 平掉。
func TestFlattenClearsSpotAndSwapOfOneAccount(t *testing.T) {
	fixture := newMixedFlattenFixture(t)

	_, err := fixture.service().FlattenLogicalAccount(context.Background(), flattenCommand("flatten-1"))

	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT-SPOT:SELL:2", "BTC-USDT-SWAP:SELL:3"}, placed(fixture))
	for _, spec := range fixture.orders.specs {
		require.Equal(t, spec.InstrumentID == "BTC-USDT-SWAP", spec.ReducePositionOnly, spec.InstrumentID)
	}
}

// 按比例减仓：两个标的各减一半；目标数量发起时写定并存进动作结果，之后各轮都收敛到它，
// 到位后用同一个动作号再跑不会再减一次。
func TestReduceFixesTargetsAtStartAndDoesNotReduceAgain(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	command := flattenCommand("reduce-1")
	command.ReduceRatio = shared.MustDecimal("0.5")

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionReduce), result.Action.ActionType)
	require.Equal(t, []string{"BTC-USDT-SPOT:SELL:1", "BTC-USDT-SWAP:SELL:1.5"}, placed(fixture))
	require.Equal(t, "1", result.ReduceTargets["account-a|BTC-USDT-SPOT"])
	require.Equal(t, "1.5", result.ReduceTargets["account-a|BTC-USDT-SWAP"])

	// 子单成交、持仓同步到位后重放同一个动作：已经到目标数量，不再下单，动作完成。
	fixture.orders.specs = nil
	account, err := fixture.store.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Wallet: "UNIFIED", Asset: "USDT", Available: "1000", Total: "1000"},
		{Wallet: "UNIFIED", Asset: "BTC", Available: "1", Total: "1"},
	}
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: fixture.now.UnixMilli(), LastSyncAt: fixture.now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "1.5", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: fixture.now.UnixMilli(),
		})
	}))
	// 子单已在上一轮结束，换成终态让重放能继续。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'FILLED', c_filled_quantity = c_quantity WHERE c_owner_id = 'reduce-1'`).Error)
	replay, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs, "已到目标数量，不再减仓")
	require.Equal(t, "COMPLETED", replay.Action.Status)
}

// 只平一个标的：只处理这一个，别的标的的单不撤、仓不动，也不暂停组合。
func TestFlattenSingleInstrumentLeavesOthersAndDoesNotPause(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	other := activeOrder(fixture, "spot-open", "TARGET")
	other.MarketType, other.InstrumentID = "SPOT", "BTC-USDT-SPOT"
	fixture.order(t, other)
	command := flattenCommand("delist-1")
	command.InstrumentID = "BTC-USDT-SWAP"

	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)

	require.NoError(t, err)
	require.Equal(t, string(operatordomain.ActionDelistFlatten), result.Action.ActionType)
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL:3"}, placed(fixture), "现货 BTC 不动")
	for _, entry := range fixture.trace {
		require.NotContains(t, entry, "cancel:", "别的标的的订单不撤")
	}
	logical, err := fixture.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState, "单标的清仓不暂停组合")
}

func TestFlattenConflictRules(t *testing.T) {
	running := func(actionType, instrumentID string) store.OperatorActionRecord {
		request := `{"logical_account_id":"logical-1"}`
		if instrumentID != "" {
			request = `{"logical_account_id":"logical-1","instrument_id":"` + instrumentID + `"}`
		}
		return store.OperatorActionRecord{ActionType: actionType, RequestJSON: request}
	}
	whole := FlattenCommand{}
	reduce := FlattenCommand{ReduceRatio: shared.MustDecimal("0.1")}
	delistBTC := FlattenCommand{InstrumentID: "BTC-USDT-SWAP"}

	require.True(t, flattenConflict(running("FLATTEN", ""), whole))
	require.True(t, flattenConflict(running("DELIST_FLATTEN", "BTC-USDT-SWAP"), whole), "整组清仓遇到运行中的单标的清仓算忙")
	require.True(t, flattenConflict(running("DELIST_FLATTEN", "BTC-USDT-SWAP"), reduce))
	require.True(t, flattenConflict(running("FLATTEN", ""), delistBTC), "单标的清仓遇到整组清仓算忙")
	require.True(t, flattenConflict(running("REDUCE", ""), delistBTC))
	require.True(t, flattenConflict(running("DELIST_FLATTEN", "BTC-USDT-SWAP"), delistBTC), "同一标的")
	require.False(t, flattenConflict(running("DELIST_FLATTEN", "ETH-USDT-SWAP"), delistBTC), "不同标的的单标的清仓不互相冲突")
	require.False(t, flattenConflict(running("MANUAL_ORDER", ""), whole))
}

// 同一个动作号在 FAILED、PARTIAL 后重放：目标数量沿用第一次算好的，不按当前持仓重算再减一次。
func TestReduceReplayAfterPartialKeepsOriginalTargets(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	command := flattenCommand("reduce-2")
	command.ReduceRatio = shared.MustDecimal("0.5")
	first, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT-SPOT:SELL:1", "BTC-USDT-SWAP:SELL:1.5"}, placed(fixture))

	for _, status := range []string{"PARTIAL", "FAILED"} {
		// 第一轮成交了一部分：现货剩 1.5（目标 1），合约剩 2.25（目标 1.5）。
		fixture.orders.specs = nil
		require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = ? WHERE c_action_id = 'reduce-2'`, status).Error)
		require.NoError(t, fixture.store.DBForTest().Exec(`DELETE FROM t_trade_orders WHERE c_owner_id = 'reduce-2'`).Error)
		account, err := fixture.store.GetTradingAccountByID(context.Background(), "account-a")
		require.NoError(t, err)
		account.Snapshot.Balances = []store.AssetBalance{
			{Wallet: "UNIFIED", Asset: "USDT", Available: "1000", Total: "1000"},
			{Wallet: "UNIFIED", Asset: "BTC", Available: "1.5", Total: "1.5"},
		}
		require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
			if err := tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
				Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: fixture.now.UnixMilli(), LastSyncAt: fixture.now.UnixMilli(),
			}); err != nil {
				return err
			}
			return tx.UpsertPosition(store.PositionRecord{
				SpaceID: "space-1", TradingAccountID: "account-a", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
				SignedQuantity: "2.25", EntryPrice: "100", MarkPrice: "100", Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: fixture.now.UnixMilli(),
			})
		}))

		_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)

		require.NoError(t, err, status)
		require.Equal(t, []string{"BTC-USDT-SPOT:SELL:0.5", "BTC-USDT-SWAP:SELL:0.75"}, placed(fixture), "只补到原目标，不按当前持仓再减一半（%s）", status)
	}
	require.Equal(t, "1", first.ReduceTargets["account-a|BTC-USDT-SPOT"])
}

// 只平一个标的时，账户里别的已不可交易的受管现货不算这次的剩余，不会让清仓空转。
func TestSingleInstrumentRemainingIgnoresOtherDelistedSpotAsset(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	ctx := context.Background()
	require.NoError(t, fixture.store.Transaction(ctx, func(tx *store.Tx) error {
		eth := fixture.instrument("account-a")
		eth.MarketType, eth.ExchangeSymbol, eth.InstrumentID, eth.BaseAsset, eth.Status = "SPOT", "ETHUSDT", "ETH-USDT-SPOT", "ETH", "BREAK"
		if err := tx.UpsertInstrument(eth); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", TradingAccountID: "account-a", OrderID: "bought-eth", ClientOrderID: "bought-eth",
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "ETHUSDT", InstrumentID: "ETH-USDT-SPOT",
			OrderType: "MARKET", Side: "BUY", Quantity: "4", FilledQuantity: "4", ReferencePrice: "100",
			State: "FILLED", OwnerType: "OPERATOR", OwnerID: "action-0", LogicalAccountID: "logical-1", Version: 1,
		})
	}))
	account, err := fixture.store.GetTradingAccountByID(ctx, "account-a")
	require.NoError(t, err)
	account.Snapshot.Balances = append(account.Snapshot.Balances, store.AssetBalance{Wallet: "UNIFIED", Asset: "ETH", Available: "4", Total: "4"})
	require.NoError(t, fixture.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, Snapshot: account.Snapshot, SnapshotSourceTime: fixture.now.UnixMilli(), LastSyncAt: fixture.now.UnixMilli(),
		})
	}))
	command := flattenCommand("delist-2")
	command.InstrumentID = "BTC-USDT-SWAP"

	result, err := fixture.service().FlattenLogicalAccount(ctx, command)

	require.NoError(t, err)
	for _, remaining := range result.Accounts[0].Remaining {
		require.NotEqual(t, "ETH", remaining.Asset, "范围外的已下架现货不算剩余")
	}
	require.Equal(t, []string{"BTC-USDT-SWAP:SELL:3"}, placed(fixture))
}

// 只平的标的在所有成员里都解析不到时不能静默完成；比例和标的不能同时给。
func TestFlattenScopeValidation(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	missing := flattenCommand("delist-3")
	missing.InstrumentID = "NOPE-USDT-SWAP"
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), missing)
	require.ErrorIs(t, err, ErrInvalidCommand)

	both := flattenCommand("delist-4")
	both.InstrumentID, both.ReduceRatio = "BTC-USDT-SWAP", shared.MustDecimal("0.5")
	_, err = fixture.service().FlattenLogicalAccount(context.Background(), both)
	require.ErrorIs(t, err, ErrInvalidCommand)
}

// 运行中的减仓遇到比例不同的减仓请求：返回忙，不能把别的比例的进度当成这次的。
func TestRunningReduceWithDifferentRatioIsBusy(t *testing.T) {
	running := store.OperatorActionRecord{
		ActionID: "r1", ActionType: "REDUCE", Status: "RUNNING",
		RequestJSON: `{"logical_account_id":"logical-1","reduce_ratio":"0.1"}`,
	}
	same := FlattenCommand{ReduceRatio: shared.MustDecimal("0.1")}
	other := FlattenCommand{ReduceRatio: shared.MustDecimal("0.3")}

	_, handled, err := runningFlattenFor(running, same)
	require.True(t, handled)
	require.NoError(t, err)
	_, handled, err = runningFlattenFor(running, other)
	require.True(t, handled)
	require.ErrorIs(t, err, ErrLogicalAccountBusy)
}
