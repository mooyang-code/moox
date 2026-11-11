package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func seedLiveSummaryFills(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	account := testAccount()
	account.ExecutionMode, account.Environment, account.AccountMode = "LIVE", "PRODUCTION", "BINANCE_STANDARD"
	account.Markets = []string{"SWAP"}
	account.MarginMode = "CROSS"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", OwnerInstanceID: "r", OwnerSessionID: "s",
			ExecutionMode: "LIVE", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "x",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: "BTC",
			PriceTick: "0.01", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", Status: "TRADING",
		}); err != nil {
			return err
		}
		for _, id := range []string{"op-order", "ext-order"} {
			owner, logical := "OPERATOR", "logical-1"
			if id == "ext-order" {
				owner, logical = "EXTERNAL", ""
			}
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", OrderID: id, ClientOrderID: id, Exchange: "BINANCE", MarketType: "SWAP",
				ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP", OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
				Quantity: "10", ReferencePrice: "100", OwnerType: owner, OwnerID: id, LogicalAccountID: logical, State: "OPEN", Version: 1,
			}); err != nil {
				return err
			}
		}
		for _, fill := range []FillRecord{
			{FillID: "f1", ExchangeTradeID: "t1", OrderID: "op-order", Fee: "0.10", FeeAsset: "USDT", RealizedPnL: "5.5", TradedAt: 1_000},
			{FillID: "f2", ExchangeTradeID: "t2", OrderID: "op-order", Fee: "0.0002", FeeAsset: "BNB", RealizedPnL: "-2", TradedAt: 2_000},
			{FillID: "f3", ExchangeTradeID: "t3", OrderID: "op-order", Fee: "0.05", FeeAsset: "USDT", RealizedPnL: "0", TradedAt: 3_000},
			{FillID: "f4", ExchangeTradeID: "t4", OrderID: "ext-order", Fee: "1", FeeAsset: "USDT", RealizedPnL: "100", TradedAt: 2_500},
		} {
			fill.SpaceID, fill.TradingAccountID, fill.Exchange, fill.MarketType = "space-1", "account-1", "BINANCE", "SWAP"
			fill.InstrumentID, fill.ExchangeSymbol, fill.Side, fill.PositionSide = "BTC-USDT-SWAP", "BTCUSDT", "BUY", "NET"
			fill.Price, fill.Quantity, fill.SettlementAsset = "100", "1", "USDT"
			if _, err := tx.InsertFill(fill); err != nil {
				return err
			}
		}
		return nil
	}))
}

// 手续费按手续费资产分别汇总（不折算），已实现盈亏按结算资产汇总；十进制累加不丢精度。
func TestSummarizeFillsSumsByAssetWithoutRounding(t *testing.T) {
	s := openTestStore(t)
	seedLiveSummaryFills(t, s)

	byAccount, err := s.SummarizeFills(context.Background(), "space-1", FillSummaryQuery{TradingAccountID: "account-1", StartTime: 0, EndTime: 10_000})
	require.NoError(t, err)
	require.Equal(t, int64(4), byAccount.FillCount)
	require.Equal(t, "0.0002", amountOf(byAccount.Fees, "BNB"))
	require.Equal(t, "1.15", amountOf(byAccount.Fees, "USDT"))
	require.Equal(t, "103.5", amountOf(byAccount.RealizedPnL, "USDT"))

	// 按组合：只算归属这个组合的订单的成交（外部单不算）。
	byLogical, err := s.SummarizeFills(context.Background(), "space-1", FillSummaryQuery{LogicalAccountID: "logical-1", EndTime: 10_000})
	require.NoError(t, err)
	require.Equal(t, int64(3), byLogical.FillCount)
	require.Equal(t, "0.15", amountOf(byLogical.Fees, "USDT"))
	require.Equal(t, "3.5", amountOf(byLogical.RealizedPnL, "USDT"))

	// 时间窗：[1500, 2600] 只含 f2、f4。
	window, err := s.SummarizeFills(context.Background(), "space-1", FillSummaryQuery{TradingAccountID: "account-1", StartTime: 1_500, EndTime: 2_600})
	require.NoError(t, err)
	require.Equal(t, int64(2), window.FillCount)

	_, err = s.SummarizeFills(context.Background(), "space-1", FillSummaryQuery{})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

// 成交写入时带上所属订单的组合账户（外部单为空）；按组合查成交、汇总直接按这一列取，成员移出后历史成交仍算在组合下。
func TestFillsCarryOrderLogicalAccount(t *testing.T) {
	s := openTestStore(t)
	seedLiveSummaryFills(t, s)
	ctx := context.Background()

	external, _, err := s.ListFills(ctx, "space-1", FillQuery{OrderID: "ext-order", Limit: 10})
	require.NoError(t, err)
	require.Len(t, external, 1)
	require.Empty(t, external[0].LogicalAccountID, "外部单的成交不属于任何组合")
	var nullCount int64
	require.NoError(t, s.db.Raw(`SELECT COUNT(*) FROM t_order_fills WHERE c_logical_account_id IS NULL`).Scan(&nullCount).Error)
	require.Equal(t, int64(1), nullCount, "没有组合时存 NULL，不存空串")

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.DeleteLogicalAccountMember("space-1", "logical-1", "account-1")
	}))
	fills, total, err := s.ListFillsReadOnly(ctx, "space-1", FillQuery{LogicalAccountID: "logical-1", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, int64(3), total, "移出的成员的历史成交仍算在组合下")
	require.Equal(t, []string{"f3", "f2"}, []string{fills[0].FillID, fills[1].FillID}, "按成交时间倒序分页")
	require.Equal(t, "logical-1", fills[0].LogicalAccountID)
	summary, err := s.SummarizeFills(ctx, "space-1", FillSummaryQuery{LogicalAccountID: "logical-1", EndTime: 10_000})
	require.NoError(t, err)
	require.Equal(t, int64(3), summary.FillCount)
}

// 启用成员时外部单被补上组合编号，它已有的成交同样补上，成交的组合编号始终跟随订单。
func TestAdoptedOrderFillsFollowLogicalAccount(t *testing.T) {
	s := openTestStore(t)
	seedLiveSummaryFills(t, s)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		})
	}))
	order, err := s.GetOrder(ctx, "space-1", "ext-order")
	require.NoError(t, err)
	require.Equal(t, "logical-1", order.LogicalAccountID)
	fills, total, err := s.ListFills(ctx, "space-1", FillQuery{LogicalAccountID: "logical-1", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	for _, fill := range fills {
		require.Equal(t, "logical-1", fill.LogicalAccountID, fill.FillID)
	}
}

func amountOf(values []AssetAmount, asset string) string {
	for _, value := range values {
		if value.Asset == asset {
			return value.Amount.String()
		}
	}
	return ""
}

func TestListOperatorActionsFiltersAndPages(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "")
	ctx := context.Background()
	for _, action := range []OperatorActionRecord{
		{ActionID: "a1", ActionType: "FLATTEN"}, {ActionID: "a2", ActionType: "REDUCE"}, {ActionID: "a3", ActionType: "FLATTEN"},
	} {
		action.SpaceID, action.LogicalAccountID, action.Reason, action.RequestJSON, action.Status = "space-1", "logical-1", "r", `{}`, "COMPLETED"
		_, _, err := s.CreateOperatorAction(ctx, action)
		require.NoError(t, err)
	}
	all, total, err := s.ListOperatorActions(ctx, "space-1", OperatorActionQuery{LogicalAccountID: "logical-1", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, all, 3)

	flatten, total, err := s.ListOperatorActions(ctx, "space-1", OperatorActionQuery{LogicalAccountID: "logical-1", ActionType: "FLATTEN", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, int64(2), total, "总数不受分页影响")
	require.Len(t, flatten, 1)

	none, total, err := s.ListOperatorActions(ctx, "space-1", OperatorActionQuery{LogicalAccountID: "other", Limit: 10})
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, none)
}
