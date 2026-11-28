package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
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

// 名单、持有者与下架记录的查询都是全局方法：没有空间头也能调用。
func TestDelistListMethodsAreGlobalAndNeedNoSpace(t *testing.T) {
	server, wakes := delistServer(t)
	ctx := context.Background() // 没有空间
	delistAt := time.Now().Add(72 * time.Hour).Truncate(time.Millisecond)

	intake, err := server.Delist.Intake(ctx, delistapp.Event{
		EventID: "evt-1", OccurredAt: time.Now(), Key: delistapp.Key{Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT"},
		Scope: store.DelistScopePaperOnly, DelistAt: delistAt, Source: "MANUAL",
	})
	require.NoError(t, err)
	require.Equal(t, 1, *wakes, "写入名单后唤醒目标重新判断")

	list, err := server.ListBlockedInstruments(ctx, &tradepb.ListBlockedInstrumentsReq{})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, list.GetRetInfo().GetCode())
	require.Len(t, list.GetInstruments(), 1)
	require.Equal(t, "binance", list.GetInstruments()[0].GetExchange())
	require.Equal(t, store.DelistScopePaperOnly, list.GetInstruments()[0].GetScope())
	require.Equal(t, intake.DelistID, list.GetInstruments()[0].GetDelistId())

	delists, err := server.ListDelists(ctx, &tradepb.ListDelistsReq{ActiveOnly: true})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, delists.GetRetInfo().GetCode())
	require.Len(t, delists.GetDelists(), 1)
	got := delists.GetDelists()[0]
	require.Equal(t, intake.DelistID, got.GetDelistId())
	require.Equal(t, "spot", got.GetMarketType())
	require.Equal(t, delistAt.UnixMilli(), got.GetDelistAt())
	require.Equal(t, store.DelistActive, got.GetStatus())
	require.Equal(t, "evt-1", got.GetSourceEventId())

	holders, err := server.ListInstrumentHolders(ctx, &tradepb.ListInstrumentHoldersReq{
		Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, holders.GetRetInfo().GetCode(), "未知标的不算失败")
	require.True(t, holders.GetInstrumentUnknown())
	require.NotNil(t, holders.GetHolders(), "holders 必须是空数组，不是省略")
	require.Empty(t, holders.GetHolders())

	removed, err := server.UnblockInstrument(ctx, &tradepb.UnblockInstrumentReq{
		Exchange: "binance", MarketType: "spot", ExchangeSymbol: "FOOUSDT",
	})
	require.NoError(t, err)
	require.True(t, removed.GetRemoved())
	require.Equal(t, 2, *wakes)
	delists, err = server.ListDelists(ctx, &tradepb.ListDelistsReq{})
	require.NoError(t, err)
	require.Equal(t, store.DelistCanceled, delists.GetDelists()[0].GetStatus(), "解除名单一并取消进行中的下架记录")
}

func TestDelistRequestsAreValidated(t *testing.T) {
	server, _ := delistServer(t)
	rsp, err := server.UnblockInstrument(context.Background(), &tradepb.UnblockInstrumentReq{ExchangeSymbol: "FOOUSDT"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	listed, err := server.ListDelists(context.Background(), &tradepb.ListDelistsReq{Limit: 501})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, listed.GetRetInfo().GetCode())
}

func liveLedgerServer(t *testing.T, adapters adapterMap) *ConsoleServer {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "live", Exchange: "binance", Markets: []string{"swap"},
			ExecutionMode: "LIVE", Environment: "PRODUCTION", CredentialSecretID: "secret-1", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	return &ConsoleServer{Store: tradeStore, Adapters: adapters}
}

type pnlLedgerAdapter struct {
	execution.ExecutionAdapter
	pages []exchange.LedgerPage
	calls []exchange.LedgerQuery
}

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
	server := liveLedgerServer(t, nil)
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

// 翻页挪不动、没取完：标明截断并写明说明。
func TestSummarizeAccountPnlTruncatesIncompletePages(t *testing.T) {
	server := liveLedgerServer(t, nil)
	end := time.Now().UTC()
	stuck := exchange.LedgerPage{HasMore: true, Entries: []exchange.LedgerEntry{funding("1", end.Add(-time.Hour).UnixMilli(), "1")}}
	adapter := &pnlLedgerAdapter{pages: []exchange.LedgerPage{stuck}}
	server.Adapters = adapterMap{"account-1": adapter}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")

	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{
		TradingAccountId: "account-1", EndTime: end.UnixMilli(), StartTime: end.Add(-30 * 24 * time.Hour).UnixMilli(),
	})

	require.NoError(t, err)
	require.True(t, rsp.GetFundingTruncated())
	require.Contains(t, rsp.GetFundingNote(), "流水条数太多")
	require.Equal(t, end.Add(-time.Hour).UnixMilli(), rsp.GetFundingFrom(), "统计起点是已取到的最老时间")
}

// 多个账户都没取完时，统计起点取各账户已取到的最老时间里最晚的一个（这之后所有账户才都取全了），不是最早的。
func TestSummarizeAccountPnlFundingFromIsTheLatestOfTheOldestTimes(t *testing.T) {
	server := liveLedgerServer(t, nil)
	require.NoError(t, server.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-2", Name: "live-2", Exchange: "binance", Markets: []string{"swap"},
			ExecutionMode: "LIVE", Environment: "PRODUCTION", CredentialSecretID: "secret-2", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	require.NoError(t, server.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "l",
			Environment: "PRODUCTION", SettlementAsset: "USDT", PauseReason: "x",
		}); err != nil {
			return err
		}
		for _, id := range []string{"account-1", "account-2"} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: id, Enabled: true, Priority: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	end := time.Now().UTC()
	older, newer := end.Add(-10*time.Hour).UnixMilli(), end.Add(-2*time.Hour).UnixMilli()
	server.Adapters = adapterMap{
		"account-1": &pnlLedgerAdapter{pages: []exchange.LedgerPage{{HasMore: true, Entries: []exchange.LedgerEntry{funding("a", older, "1")}}}},
		"account-2": &pnlLedgerAdapter{pages: []exchange.LedgerPage{{HasMore: true, Entries: []exchange.LedgerEntry{funding("b", newer, "1")}}}},
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")

	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{
		LogicalAccountId: "logical-1", EndTime: end.UnixMilli(), StartTime: end.Add(-24 * time.Hour).UnixMilli(),
	})

	require.NoError(t, err)
	require.True(t, rsp.GetFundingTruncated())
	require.Equal(t, newer, rsp.GetFundingFrom())
	// 合计只算统计起点之后的流水：10 小时前那一条（只有一个账户取到）不计入，否则合计和起点对不上。
	require.Len(t, rsp.GetFundingFees(), 1)
	require.Equal(t, "1", rsp.GetFundingFees()[0].GetAmount())
}

func TestSummarizeAccountPnlWithoutLedgerExplainsWhy(t *testing.T) {
	server := liveLedgerServer(t, adapterMap{})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := server.SummarizeAccountPnl(ctx, &tradepb.SummarizeAccountPnlReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.False(t, rsp.GetFundingAvailable())
	require.NotEmpty(t, rsp.GetFundingNote())
	require.NotNil(t, rsp.GetFundingFees())
}
