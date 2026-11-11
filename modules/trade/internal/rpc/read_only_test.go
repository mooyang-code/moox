package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 管理台组合详情页、列表页轮询的查询接口经只读视图读库：读写连接上开着没提交的事务（例如目标执行正在写）时，
// 照样在短超时内返回，读到的是已提交的数据。
func TestConsoleQueriesDoNotWaitForReadWriteConnection(t *testing.T) {
	tradeStore := openRPCStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "account-1",
			Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED",
			Snapshot: store.TradingAccountSnapshot{Balances: []store.AssetBalance{
				{Wallet: string(exchange.WalletUnified), Asset: "USDT", Total: "500", Available: "500"},
			}, ExchangeUpdatedAt: 1_000},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode: "PAPER", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		})
	}))
	_, err := tradeStore.BlockInstrument(context.Background(), store.BlockedInstrumentRecord{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "DOGEUSDT", Reason: "下架", BlockedAt: 1,
	})
	require.NoError(t, err)
	server := &ConsoleServer{
		AccountServer: &AccountServer{Store: tradeStore},
		LogicalAccountServer: &LogicalAccountServer{
			Store: tradeStore, LogicalAccounts: &logicalapp.Service{Store: tradeStore},
		},
		ExecutionServer: &ExecutionServer{Store: tradeStore},
		Store:           tradeStore,
		Holdings:        &HoldingQuery{Store: tradeStore.ReadOnly(), Adapters: holdingAdaptersStub{err: errors.New("会话未启动")}},
		Delist:          &delistapp.Service{Store: tradeStore},
	}

	require.NoError(t, tradeStore.DBForTest().Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`UPDATE t_logical_accounts SET c_name = 'pending'`).Error)
		ctx, cancel := context.WithTimeout(spacecontext.WithSpaceID(context.Background(), "space-1"), 2*time.Second)
		defer cancel()
		started := time.Now()

		logical, err := server.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, logical.GetRetInfo().GetCode(), logical.GetRetInfo())
		require.Equal(t, "logical", logical.GetLogicalAccount().GetName(), "看不到没提交的修改")
		require.Len(t, logical.GetLogicalAccount().GetMembers(), 1)
		require.NotEmpty(t, logical.GetLogicalAccount().GetMarkets())
		// 就绪原因由成员、账户、挂单、目标几次读库得出，读失败会折进原因里，要逐条确认没有超时
		require.NotContains(t, strings.Join(logical.GetLogicalAccount().GetReadinessReasons(), "；"), "deadline")

		logicals, err := server.ListLogicalAccounts(ctx, &tradepb.ListLogicalAccountsReq{})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, logicals.GetRetInfo().GetCode(), logicals.GetRetInfo())
		require.Len(t, logicals.GetLogicalAccounts(), 1)
		require.NotContains(t, strings.Join(logicals.GetLogicalAccounts()[0].GetReadinessReasons(), "；"), "deadline")

		target, err := server.GetLogicalAccountTarget(ctx, &tradepb.GetLogicalAccountTargetReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_NOT_FOUND, target.GetRetInfo().GetCode(), target.GetRetInfo())

		accounts, err := server.ListTradingAccounts(ctx, &tradepb.ListTradingAccountsReq{})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, accounts.GetRetInfo().GetCode(), accounts.GetRetInfo())
		require.Len(t, accounts.GetAccounts(), 1)

		positions, err := server.ListPositions(ctx, &tradepb.ListPositionsReq{LogicalAccountId: "logical-1", InstrumentId: "BTC-USDT"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, positions.GetRetInfo().GetCode(), positions.GetRetInfo())

		holdings, err := server.ListHoldings(ctx, &tradepb.ListHoldingsReq{TradingAccountId: "account-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, holdings.GetRetInfo().GetCode(), holdings.GetRetInfo())
		require.Len(t, holdings.GetHoldings(), 1)

		orders, err := server.ListOrders(ctx, &tradepb.ListOrdersReq{LogicalAccountId: "logical-1", InstrumentId: "BTC-USDT"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, orders.GetRetInfo().GetCode(), orders.GetRetInfo())

		curve, err := server.QueryEquityCurve(ctx, &tradepb.QueryEquityCurveReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, curve.GetRetInfo().GetCode(), curve.GetRetInfo())

		blocked, err := server.ListBlockedInstruments(ctx, &tradepb.ListBlockedInstrumentsReq{})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, blocked.GetRetInfo().GetCode(), blocked.GetRetInfo())
		require.Len(t, blocked.GetInstruments(), 1)

		// 人工操作进行中时页面每 1.5 秒查一次订单和操作记录：查不到是“不存在”，不能是等连接超时
		order, err := server.GetOrder(ctx, &tradepb.GetOrderReq{OrderId: "missing"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_NOT_FOUND, order.GetRetInfo().GetCode(), order.GetRetInfo())
		action, err := server.GetOperatorAction(ctx, &tradepb.GetOperatorActionReq{ActionId: "missing"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_NOT_FOUND, action.GetRetInfo().GetCode(), action.GetRetInfo())

		fills, err := server.ListFills(ctx, &tradepb.ListFillsReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, fills.GetRetInfo().GetCode(), fills.GetRetInfo())

		actions, err := server.ListOperatorActions(ctx, &tradepb.ListOperatorActionsReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, actions.GetRetInfo().GetCode(), actions.GetRetInfo())

		pnl, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_SUCCESS, pnl.GetRetInfo().GetCode(), pnl.GetRetInfo())

		require.Less(t, time.Since(started), time.Second)
		return nil
	}))
}
