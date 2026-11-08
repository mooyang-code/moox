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
