package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	executionpaper "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

// 模拟盘的资金费从 t_paper_funding_fees 推出流水，资金流水和收益拆解都能汇总到，合计与表里一致。
func TestPaperFundingFeesShowInLedgerAndPnl(t *testing.T) {
	server := ledgerServer(t, nil)
	ctx := context.Background()
	account, err := server.Store.GetTradingAccount(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.NoError(t, server.Store.Transaction(ctx, func(tx *store.Tx) error {
		for symbol, quantity := range map[string]string{"BTCUSDT": "2", "ETHUSDT": "-3"} {
			if err := tx.UpsertPosition(store.PositionRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP", ExchangeSymbol: symbol, PositionSide: "NET",
				SignedQuantity: quantity, EntryPrice: "100", MarkPrice: "100", Leverage: "10", MarginMode: "CROSS", ExchangeUpdatedAt: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	end := time.Now().UTC().Truncate(time.Hour)
	rate := func(symbol string, at time.Time, value, mark string) exchange.FundingRate {
		return exchange.FundingRate{ExchangeSymbol: symbol, FundingTime: at, Rate: shared.MustDecimal(value), MarkPrice: shared.MustDecimal(mark)}
	}
	inserted, err := server.Store.SettlePaperFunding(ctx, "space-1", "account-1", []exchange.FundingRate{
		rate("BTCUSDT", end.Add(-8*time.Hour), "0.0001", "50000"), // -10
		rate("ETHUSDT", end.Add(-8*time.Hour), "0.0002", "2000"),  // +1.2
		rate("BTCUSDT", end, "-0.00005", "40000"),                 // +4
	})
	require.NoError(t, err)
	require.Len(t, inserted, 3)
	server.Adapters = adapterMap{"account-1": &executionpaper.Adapter{Account: account, Store: server.Store}}
	spaceCtx := spacecontext.WithSpaceID(ctx, "space-1")

	ledger, err := server.ListAccountLedger(spaceCtx, &tradepb.ListAccountLedgerReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, ledger.GetRetInfo().GetCode(), ledger.GetRetInfo().GetMsg())
	require.Len(t, ledger.GetEntries(), 3)
	require.Equal(t, "4", ledger.GetEntries()[0].GetAmount())
	require.Equal(t, "FUNDING_FEE", ledger.GetEntries()[0].GetType())
	require.Equal(t, tradepb.MarketType_MARKET_TYPE_SWAP, ledger.GetEntries()[0].GetMarketType())

	pnl, err := server.SummarizeAccountPnl(spaceCtx, &tradepb.SummarizeAccountPnlReq{
		TradingAccountId: "account-1", StartTime: end.Add(-24 * time.Hour).UnixMilli(), EndTime: end.Add(time.Minute).UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, pnl.GetRetInfo().GetCode(), pnl.GetRetInfo().GetMsg())
	require.True(t, pnl.GetFundingAvailable())
	require.False(t, pnl.GetFundingTruncated())
	require.Len(t, pnl.GetFundingFees(), 1)
	require.Equal(t, "USDT", pnl.GetFundingFees()[0].GetAsset())
	require.Equal(t, "-4.8", pnl.GetFundingFees()[0].GetAmount())
}

// 资金费记录超过流水翻页上限（5 页 × 1000 条）时，收益拆解的合计仍然准确、不标截断：模拟盘直接汇总表，不经过流水翻页。
func TestPaperFundingPnlIsExactBeyondLedgerPageLimit(t *testing.T) {
	server := ledgerServer(t, adapterMap{})
	ctx := context.Background()
	symbols := []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"}
	require.NoError(t, server.Store.Transaction(ctx, func(tx *store.Tx) error {
		for _, symbol := range symbols {
			if err := tx.UpsertPosition(store.PositionRecord{
				SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP", ExchangeSymbol: symbol, PositionSide: "NET",
				SignedQuantity: "1", EntryPrice: "100", MarkPrice: "100", Leverage: "10", MarginMode: "CROSS", ExchangeUpdatedAt: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	end := time.Now().UTC().Truncate(time.Hour)
	var rates []exchange.FundingRate
	for hour := 0; hour < 1800; hour++ {
		for _, symbol := range symbols {
			rates = append(rates, exchange.FundingRate{ExchangeSymbol: symbol, FundingTime: end.Add(-time.Duration(hour) * time.Hour),
				Rate: shared.MustDecimal("0.0001"), MarkPrice: shared.MustDecimal("100.1")})
		}
	}
	inserted, err := server.Store.SettlePaperFunding(ctx, "space-1", "account-1", rates)
	require.NoError(t, err)
	require.Len(t, inserted, 5400)

	pnl, err := server.SummarizeAccountPnl(spacecontext.WithSpaceID(ctx, "space-1"), &tradepb.SummarizeAccountPnlReq{
		TradingAccountId: "account-1", StartTime: end.Add(-80 * 24 * time.Hour).UnixMilli(), EndTime: end.Add(time.Minute).UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, pnl.GetRetInfo().GetCode(), pnl.GetRetInfo().GetMsg())
	require.True(t, pnl.GetFundingAvailable())
	require.False(t, pnl.GetFundingTruncated(), pnl.GetFundingNote())
	require.Len(t, pnl.GetFundingFees(), 1)
	// 每条 -1 × 100.1 × 0.0001 = -0.01001，共 5400 条。
	require.Equal(t, "-54.054", pnl.GetFundingFees()[0].GetAmount())
}

// OKX 模拟永续不结算资金费：合计为空，说明里写明，而不是悄悄显示 0。
func TestPaperFundingPnlNotesOkxIsNotSettled(t *testing.T) {
	server := ledgerServer(t, adapterMap{})
	require.NoError(t, server.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "okx-paper", Name: "okx-paper", Exchange: "OKX", Markets: []string{"SWAP"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}))
	pnl, err := server.SummarizeAccountPnl(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.SummarizeAccountPnlReq{TradingAccountId: "okx-paper"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, pnl.GetRetInfo().GetCode(), pnl.GetRetInfo().GetMsg())
	require.Empty(t, pnl.GetFundingFees())
	require.Contains(t, pnl.GetFundingNote(), "OKX 模拟盘暂不结算资金费")
}
