package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestMergeTransferEntriesKeepsOnlyCompletedAndNewestFirst(t *testing.T) {
	exchangeEntries := []exchange.LedgerEntry{
		{ID: "1", Time: time.UnixMilli(3000), Type: exchange.LedgerFee, Asset: "USDT", Amount: shared.MustDecimal("-0.1")},
		{ID: "2", Time: time.UnixMilli(1000), Type: exchange.LedgerFundingFee, Asset: "USDT", Amount: shared.MustDecimal("0.2")},
	}
	transfers := []store.WalletTransferRecord{
		{TransferID: "done", Direction: store.TransferToSpot, Asset: "USDT", Amount: "500", State: store.TransferStateCompleted, RequestedAt: 1500, CompletedAt: 2000},
		{TransferID: "pending", Direction: store.TransferToSpot, Asset: "USDT", Amount: "10", State: store.TransferStatePending, RequestedAt: 2500},
		{TransferID: "failed", Direction: store.TransferToFutures, Asset: "USDT", Amount: "10", State: store.TransferStateFailed, RequestedAt: 2600},
		{TransferID: "back", Direction: store.TransferToFutures, Asset: "USDT", Amount: "499", State: store.TransferStateCompleted, RequestedAt: 3500, CompletedAt: 3600},
	}
	merged, hasMore := mergeTransferEntries(exchangeEntries, transfers, 10, false)
	require.False(t, hasMore)
	require.Len(t, merged, 4, "只合并已完成的划转")
	require.Equal(t, []string{"transfer:back", "1", "transfer:done", "2"}, []string{merged[0].ID, merged[1].ID, merged[2].ID, merged[3].ID})
	require.Equal(t, exchange.LedgerTransfer, merged[0].Type)
	require.Equal(t, exchange.MarketTypeUnspecified, merged[0].Market, "划转是账户级流水")
	require.Contains(t, merged[0].Info, "归集")

	limited, hasMore := mergeTransferEntries(exchangeEntries, transfers, 2, false)
	require.Len(t, limited, 2)
	require.True(t, hasMore)
}

func TestListAccountLedgerMergesLocalTransfers(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	tradeStore := openRPCStore(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "dual-1", Name: "dual", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "LIVE", AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION", CredentialSecretID: "s", SettlementAsset: "USDT",
			MarginMode: "CROSS", Status: "ENABLED", LeverageSettings: store.LeverageSettings{store.DefaultLeverageKey: "1"},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "l", OwnerInstanceID: "r", OwnerSessionID: "s",
			ExecutionMode: "LIVE", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "x",
		}); err != nil {
			return err
		}
		return tx.InsertWalletTransfer(store.WalletTransferRecord{
			SpaceID: "space-1", TransferID: "t-1", TradingAccountID: "dual-1", LogicalAccountID: "logical-1", TargetID: "target-1",
			Direction: store.TransferToSpot, Asset: "USDT", Amount: "500", ClientRef: "target-1:TO_SPOT:1", State: store.TransferStateCompleted,
			RequestedAt: time.Now().Add(-time.Hour).UnixMilli(), CompletedAt: time.Now().Add(-time.Hour).UnixMilli(),
		})
	}))
	adapter := &marketLedgerAdapter{}
	multi, err := execution.NewMultiMarket("BINANCE", []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}, map[exchange.MarketType]execution.ExecutionAdapter{
		exchange.MarketTypeSpot: &marketLedgerAdapter{}, exchange.MarketTypeSwap: adapter,
	})
	require.NoError(t, err)
	handler := &ConsoleServer{Store: tradeStore, Adapters: singleLedgerSource{adapter: multi}}
	rsp, err := handler.ListAccountLedger(ctx, &tradepb.ListAccountLedgerReq{
		TradingAccountId: "dual-1", MarketType: tradepb.MarketType_MARKET_TYPE_SWAP,
		StartTime: time.Now().Add(-24 * time.Hour).UnixMilli(), EndTime: time.Now().UnixMilli(),
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	ids := map[string]bool{}
	for _, entry := range rsp.GetEntries() {
		ids[entry.GetId()] = true
	}
	require.True(t, ids["transfer:t-1"], "本地划转记录应合并进资金流水：%v", ids)
	require.Len(t, rsp.GetEntries(), 3)
}
