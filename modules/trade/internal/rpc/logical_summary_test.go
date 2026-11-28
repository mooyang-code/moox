package rpc

import (
	"context"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestListLogicalAccountsIncludesAggregatedPositionCountAnd24hChange(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	st := openRPCStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	logicalSrv := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: st, Syncer: noopSyncer{}, Now: func() time.Time { return now }},
		Store:           st,
		Now:             func() time.Time { return now },
	}
	accountSrv := &AccountServer{
		Accounts: &accountapp.Service{Store: st},
		Store:    st,
	}

	created, err := accountSrv.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "paper-summary", Exchange: "binance",
		Markets: []string{"spot", "swap"}, SettlementAsset: "USDT", MarginMode: "CROSS",
		Paper: &tradepb.PaperConfig{
			InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
		},
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo())
	accID := created.GetAccount().GetTradingAccountId()
	require.NoError(t, st.DBForTest().Exec(
		"UPDATE t_trading_accounts SET c_ready = 1, c_last_sync_at = ?, c_snapshot_source_time = ? WHERE c_trading_account_id = ?",
		now.UnixMilli(), now.UnixMilli(), accID,
	).Error)

	createdLogical, err := logicalSrv.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name:            "组合摘要",
		Environment:     tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
		SettlementAsset: "USDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, createdLogical.GetRetInfo().GetCode(), createdLogical.GetRetInfo())
	logID := createdLogical.GetLogicalAccount().GetLogicalAccountId()

	added, err := logicalSrv.AddLogicalAccountMember(ctx, &tradepb.AddLogicalAccountMemberReq{
		LogicalAccountId: logID, TradingAccountId: accID, Enabled: true, Priority: 1,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, added.GetRetInfo().GetCode(), added.GetRetInfo())

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
	require.NotNil(t, item.TotalEquity)
	require.Equal(t, "1200", item.GetTotalEquity())
	require.NotNil(t, item.EquityChange_24H)
	require.Equal(t, "200", item.GetEquityChange_24H())
	require.NotNil(t, item.EquityChangePct_24H)
	require.Equal(t, "0.2", item.GetEquityChangePct_24H())

	// 仍在组合中的模拟盘账户不能直接关闭。
	blocked, err := accountSrv.UpdateTradingAccount(ctx, &tradepb.UpdateTradingAccountReq{
		TradingAccountId: accID,
		Status:           "DISABLED",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, blocked.GetRetInfo().GetCode(), blocked.GetRetInfo())

	// 清空敞口后移出组合，再通过 UpdateTradingAccount(DISABLED) 关闭模拟盘账户。
	require.NoError(t, st.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: accID, MarketType: string(exchange.MarketTypeSwap),
			InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", PositionSide: "NET",
			SignedQuantity: "0", EntryPrice: "50000", MarkPrice: "50000", Leverage: "1",
			ExchangeUpdatedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.UpdateTradingAccountSnapshot("space-1", accID, store.TradingAccountSnapshot{
			Balances: []store.AssetBalance{
				{Wallet: "UNIFIED", Asset: "USDT", Total: "1000", Available: "1000", Locked: "0"},
			},
			Equity: "1000", AvailableFunds: "1000",
			UsedMargin: "0", MaintenanceMargin: "0", UnrealizedPnL: "0",
			ExchangeUpdatedAt: now.UnixMilli(),
		})
	}))
	removed, err := logicalSrv.RemoveLogicalAccountMember(ctx, &tradepb.RemoveLogicalAccountMemberReq{
		LogicalAccountId: logID, TradingAccountId: accID,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, removed.GetRetInfo().GetCode(), removed.GetRetInfo())
	require.Empty(t, removed.GetLogicalAccount().GetMembers())

	closed, err := accountSrv.UpdateTradingAccount(ctx, &tradepb.UpdateTradingAccountReq{
		TradingAccountId: accID,
		Status:           "DISABLED",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, closed.GetRetInfo().GetCode(), closed.GetRetInfo())
	require.Equal(t, "DISABLED", closed.GetAccount().GetStatus())
}
