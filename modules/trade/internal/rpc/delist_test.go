package rpc

import (
	"context"
	"testing"
	"time"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func delistServer(t *testing.T) (*ConsoleServer, *int) {
	t.Helper()
	tradeStore := openRPCStore(t)
	wakes := 0
	return &ConsoleServer{
		Store:  tradeStore,
		Delist: &delistapp.Service{Store: tradeStore, OnChanged: func() { wakes++ }},
	}, &wakes
}

// 名单的三个方法和 ListInstrumentHolders 是全局方法：没有空间头也能调用。
func TestDelistListMethodsAreGlobalAndNeedNoSpace(t *testing.T) {
	server, wakes := delistServer(t)
	ctx := context.Background() // 没有空间

	blocked, err := server.BlockInstrument(ctx, &tradepb.BlockInstrumentReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT,
		ExchangeSymbol: "FOOUSDT", Reason: "公告下架", EventId: "evt-1",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, blocked.GetRetInfo().GetCode())
	require.True(t, blocked.GetInstrumentUnknown(), "没有开通对应市场的账户：照常写入，带无此标的标记")
	require.True(t, blocked.GetCreated())
	require.Equal(t, 1, *wakes, "写入名单后唤醒目标重新判断")

	list, err := server.ListBlockedInstruments(ctx, &tradepb.ListBlockedInstrumentsReq{})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, list.GetRetInfo().GetCode())
	require.Len(t, list.GetInstruments(), 1)
	require.Equal(t, tradepb.Exchange_EXCHANGE_BINANCE, list.GetInstruments()[0].GetExchange())
	require.Equal(t, "evt-1", list.GetInstruments()[0].GetSourceEventId())

	holders, err := server.ListInstrumentHolders(ctx, &tradepb.ListInstrumentHoldersReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, ExchangeSymbol: "FOOUSDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, holders.GetRetInfo().GetCode(), "未知标的不算失败")
	require.True(t, holders.GetInstrumentUnknown())
	require.NotNil(t, holders.GetHolders(), "holders 必须是空数组，不是省略")
	require.Empty(t, holders.GetHolders())

	removed, err := server.UnblockInstrument(ctx, &tradepb.UnblockInstrumentReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, ExchangeSymbol: "FOOUSDT",
	})
	require.NoError(t, err)
	require.True(t, removed.GetRemoved())
	require.Equal(t, 2, *wakes)
}

func TestDelistRequestsAreValidated(t *testing.T) {
	server, _ := delistServer(t)
	rsp, err := server.BlockInstrument(context.Background(), &tradepb.BlockInstrumentReq{ExchangeSymbol: "FOOUSDT", Reason: "r"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func logicalSeed(t *testing.T, server *ConsoleServer) {
	t.Helper()
	require.NoError(t, server.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "a", Exchange: "BINANCE", Markets: []string{"SPOT"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "l", OwnerInstanceID: "r", OwnerSessionID: "s",
			ExecutionMode: "PAPER", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "x",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "PRODUCTION", MarketType: "SPOT", ExchangeSymbol: "FOOUSDT", InstrumentID: "FOO-USDT-SPOT",
			BaseAsset: "FOO", QuoteAsset: "USDT", SettlementAsset: "USDT", PriceTick: "0.01", ExchangeQuantityStep: "0.1", MinExchangeQuantity: "0.1", Status: "TRADING",
		})
	}))
}

// FlattenInstrument：按空间调用，交易所代码由 Trade 换算成内部标的 ID；“忙”单独返回标记，不返回别人的动作。
func TestFlattenInstrumentResolvesInstrumentAndReportsBusy(t *testing.T) {
	server, _ := delistServer(t)
	logicalSeed(t, server)
	var got FlattenInstrumentCommand
	server.FlattenInstrumentAction = func(_ context.Context, command FlattenInstrumentCommand) (store.OperatorActionRecord, bool, error) {
		got = command
		return store.OperatorActionRecord{ActionID: command.ActionID, LogicalAccountID: command.LogicalAccountID, ActionType: "DELIST_FLATTEN", Status: "COMPLETED"}, true, nil
	}
	req := &tradepb.FlattenInstrumentReq{
		ActionId: "act-1", LogicalAccountId: "logical-1", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
		MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, ExchangeSymbol: "FOOUSDT", Reason: "下架",
	}

	// 没有空间头：按空间的方法要求空间。
	noSpace, err := server.FlattenInstrument(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NO_PERMISSION, noSpace.GetRetInfo().GetCode())

	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := server.FlattenInstrument(ctx, req)
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, FlattenInstrumentCommand{
		SpaceID: "space-1", ActionID: "act-1", LogicalAccountID: "logical-1", Exchange: "BINANCE", InstrumentID: "FOO-USDT-SPOT", Reason: "下架",
	}, got)
	require.True(t, rsp.GetUnblocked())
	require.Equal(t, "DELIST_FLATTEN", rsp.GetAction().GetActionType())

	// 忙：返回 busy 标记和 CONFLICT，不带动作。
	server.FlattenInstrumentAction = func(context.Context, FlattenInstrumentCommand) (store.OperatorActionRecord, bool, error) {
		return store.OperatorActionRecord{ActionID: "other"}, false, operatorapp.ErrLogicalAccountBusy
	}
	busy, err := server.FlattenInstrument(ctx, req)
	require.NoError(t, err)
	require.True(t, busy.GetBusy())
	require.Equal(t, tradepb.ErrorCode_CONFLICT, busy.GetRetInfo().GetCode())
	require.Nil(t, busy.GetAction())

	// 别的空间的组合：查不到。
	other, err := server.FlattenInstrument(spacecontext.WithSpaceID(context.Background(), "space-2"), req)
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NOT_FOUND, other.GetRetInfo().GetCode())

	// 组合里没有该交易所的成员：参数错误，不能静默当作平完。
	req.Exchange = tradepb.Exchange_EXCHANGE_OKX
	missing, err := server.FlattenInstrument(ctx, req)
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())
}

func TestReduceLogicalAccountPassesTheRatio(t *testing.T) {
	server, _ := delistServer(t)
	var ratio shared.Decimal
	server.ReduceAccount = func(_ context.Context, spaceID, actionID, logicalAccountID string, r shared.Decimal, reason string) (store.OperatorActionRecord, error) {
		ratio = r
		return store.OperatorActionRecord{ActionID: actionID, LogicalAccountID: logicalAccountID, ActionType: "REDUCE", Status: "RUNNING"}, nil
	}
	rsp, err := server.ReduceLogicalAccount(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.ReduceLogicalAccountReq{
		ActionId: "a", LogicalAccountId: "l", ReduceRatio: "0.1", Reason: "保证金率低",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "0.1", ratio.String())
	require.Equal(t, "REDUCE", rsp.GetAction().GetActionType())
}

// ---- T29 ----

type pnlLedgerAdapter struct {
	execution.ExecutionAdapter
	pages []exchange.LedgerPage
	calls []exchange.LedgerQuery
}

func (a *pnlLedgerAdapter) LedgerSupported() bool { return true }
func (a *pnlLedgerAdapter) ListLedger(_ context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	a.calls = append(a.calls, query)
	page := a.pages[0]
	if len(a.pages) > 1 {
		a.pages = a.pages[1:]
	}
	return page, nil
}

func funding(id string, at int64, amount string) exchange.LedgerEntry {
	return exchange.LedgerEntry{ID: id, Time: time.UnixMilli(at), Type: exchange.LedgerFundingFee, Asset: "USDT", Amount: shared.MustDecimal(amount)}
}

func TestSummarizeAccountPnlFundingPagesAndMarksTruncation(t *testing.T) {
	server := ledgerServer(t, nil)
	end := time.Now().UTC()
	adapter := &pnlLedgerAdapter{pages: []exchange.LedgerPage{
		{HasMore: true, Entries: []exchange.LedgerEntry{funding("1", end.Add(-time.Hour).UnixMilli(), "-0.3"), {ID: "2", Time: end.Add(-2 * time.Hour), Type: exchange.LedgerTrade, Asset: "USDT", Amount: shared.MustDecimal("5")}}},
		{HasMore: false, Entries: []exchange.LedgerEntry{funding("1", end.Add(-time.Hour).UnixMilli(), "-0.3"), funding("3", end.Add(-3*time.Hour).UnixMilli(), "0.1")}},
	}}
	server.Adapters = adapterMap{"account-1": adapter}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")

	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{TradingAccountId: "account-1", EndTime: end.UnixMilli(), StartTime: end.Add(-24 * time.Hour).UnixMilli()})

	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.True(t, rsp.GetFundingAvailable())
	require.False(t, rsp.GetFundingTruncated(), "翻页取完了，没有截断")
	require.Len(t, rsp.GetFundingFees(), 1)
	require.Equal(t, "-0.2", rsp.GetFundingFees()[0].GetAmount(), "按流水号去重：-0.3 只算一次，再加 0.1")
	require.Len(t, adapter.calls, 2, "翻页")
	require.Empty(t, rsp.GetFees(), "没有成交就没有手续费")
}

func TestSummarizeAccountPnlTruncatesLongWindowsAndIncompletePages(t *testing.T) {
	server := ledgerServer(t, nil)
	end := time.Now().UTC()
	// 每页都说还有更多、而且挪不动：翻页停下，标明截断。
	stuck := exchange.LedgerPage{HasMore: true, Entries: []exchange.LedgerEntry{funding("1", end.UnixMilli(), "1")}}
	adapter := &pnlLedgerAdapter{pages: []exchange.LedgerPage{stuck}}
	server.Adapters = adapterMap{"account-1": adapter}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")

	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{
		TradingAccountId: "account-1", EndTime: end.UnixMilli(), StartTime: end.Add(-120 * 24 * time.Hour).UnixMilli(),
	})

	require.NoError(t, err)
	require.True(t, rsp.GetFundingTruncated(), "窗口超过 90 天，且翻页没取完")
	require.Contains(t, rsp.GetFundingNote(), "90 天")
	require.Equal(t, end.Add(-maxFundingWindow).UnixMilli(), adapter.calls[0].Start.UnixMilli(), "最长只查 90 天")
}

func TestSummarizeAccountPnlWithoutLedgerExplainsWhy(t *testing.T) {
	server := ledgerServer(t, adapterMap{})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetFundingAvailable())
	require.NotEmpty(t, rsp.GetFundingNote())
	require.NotNil(t, rsp.GetFundingFees())
}
