package rpc

import (
	"context"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestListLogicalAccountsIncludesAggregatedPositionCountAnd24hChange(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	st := openRPCStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	logicalSrv := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: st},
		Store:           st,
		Now:             func() time.Time { return now },
	}
	accountSrv := &AccountServer{
		Accounts: &accountapp.Service{Store: accountapp.Repository{Store: st}},
		Store:    st,
		Logical:  logicalSrv,
	}

	created, err := accountSrv.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "paper-summary", LogicalAccountName: "组合摘要", Exchange: "binance",
		Markets: []string{"spot", "swap"}, SettlementAsset: "USDT", MarginMode: "CROSS",
		Paper: &tradepb.PaperConfig{
			InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
		},
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo())
	accID := created.GetAccount().GetTradingAccountId()
	logID := created.GetLogicalAccount().GetLogicalAccountId()

	require.NoError(t, st.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "binance", Environment: "PRODUCTION", MarketType: "spot",
			ExchangeSymbol: "ETHUSDT", InstrumentID: "ETH-USDT-SPOT",
			BaseAsset: "ETH", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.UpdateTradingAccountSnapshot("space-1", accID, store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{
				{Wallet: "UNIFIED", Asset: "USDT", Total: "1000", Available: "1000", Locked: "0"},
				{Wallet: "UNIFIED", Asset: "ETH", Total: "2", Available: "2", Locked: "0"},
			},
			Equity: "1200", AvailableFunds: "1000",
			UsedMargin: "0", MaintenanceMargin: "0", UnrealizedPnL: "0",
			ExchangeUpdatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		if err := tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: accID, MarketType: string(exchange.MarketTypeSwap),
			InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "0.5", EntryPrice: "50000", MarkPrice: "50000", Leverage: "1",
			ExchangeUpdatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		for _, pt := range []struct {
			at     time.Time
			equity string
		}{
			{now.Add(-20 * time.Hour), "1000"},
			{now.Add(-1 * time.Hour), "1200"},
		} {
			if err := tx.UpsertLogicalAccountEquityPoint(store.EquityPointRecord{
				SpaceID: "space-1", LogicalAccountID: logID, BucketTime: pt.at.UnixMilli(),
				Equity: pt.equity, AvailableFunds: pt.equity, UsedMargin: "0", SourceTime: pt.at.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	listed, err := logicalSrv.ListLogicalAccounts(ctx, &tradepb.ListLogicalAccountsReq{})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode())
	require.Len(t, listed.GetLogicalAccounts(), 1)
	item := listed.GetLogicalAccounts()[0]
	require.Equal(t, int32(2), item.GetPositionCount(), "ETH 现货 + BTC 永续 = 2 个持仓标的")
	require.NotNil(t, item.EquityChange_24H)
	require.Equal(t, "200", item.GetEquityChange_24H())
	require.NotNil(t, item.EquityChangePct_24H)
	require.Equal(t, "0.2", item.GetEquityChangePct_24H())

	// 顺便验证通过统一 UpdateTradingAccount(DISABLED) 关闭模拟盘账户并返回受影响组合。
	closed, err := accountSrv.UpdateTradingAccount(ctx, &tradepb.UpdateTradingAccountReq{
		TradingAccountId: accID,
		Status:           "DISABLED",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, closed.GetRetInfo().GetCode(), closed.GetRetInfo())
	require.Equal(t, "DISABLED", closed.GetAccount().GetStatus())
	require.NotNil(t, closed.GetLogicalAccount())
	require.Equal(t, logID, closed.GetLogicalAccount().GetLogicalAccountId())
	require.Equal(t, "PAUSED", closed.GetLogicalAccount().GetAutomationState())
	require.Empty(t, closed.GetLogicalAccount().GetMembers())
}
