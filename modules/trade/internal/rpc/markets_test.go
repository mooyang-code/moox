package rpc

import (
	"context"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestNormalizeForMarkets(t *testing.T) {
	got, err := normalizeForMarkets("BTC-USDT", []string{"SWAP"})
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SWAP", got)

	// 开通多个市场时，标的必须带市场后缀，不能猜。
	both := []string{"SPOT", "SWAP"}
	got, err = normalizeForMarkets("btc-usdt-swap", both)
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SWAP", got)
	got, err = normalizeForMarkets("BTC-USDT-SPOT", both)
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT-SPOT", got)
	_, err = normalizeForMarkets("BTC-USDT", both)
	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
	_, err = normalizeForMarkets("BTC-USDT-SWAP", []string{"SPOT"})
	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
	_, err = normalizeForMarkets("BTC-USDT", nil)
	require.ErrorIs(t, err, exchange.ErrInvalidInstrumentID)
}

func TestAccountToPBCarriesMarketsWalletsAndAccountMode(t *testing.T) {
	record := store.TradingAccountRecord{
		TradingAccountID: "a1", SpaceID: "s", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE",
		AccountMode: "BINANCE_STANDARD", InspectionJSON: `{"withdraw":false}`,
		Snapshot: store.TradingAccountSnapshot{
			Balances:       []store.AssetBalance{{Wallet: "FUTURES", Asset: "USDT", Total: "5", Borrowed: "1"}},
			ExchangeEquity: "6",
		},
	}
	pb := accountToPB(record)
	require.Equal(t, []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT, tradepb.MarketType_MARKET_TYPE_SWAP}, pb.GetMarkets())
	require.Equal(t, "BINANCE_STANDARD", pb.GetAccountMode())
	require.JSONEq(t, `{"withdraw":false}`, pb.GetInspectionJson())
	require.Equal(t, "FUTURES", pb.GetSnapshot().GetBalances()[0].GetWallet())
	require.Equal(t, "1", pb.GetSnapshot().GetBalances()[0].GetBorrowed())
	require.Equal(t, "6", pb.GetSnapshot().GetExchangeEquity())
}

func TestLogicalAccountMarketsFollowEnabledMembers(t *testing.T) {
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	tradeStore := openRPCStore(t)
	handler := &LogicalAccountServer{LogicalAccounts: &logicalapp.Service{Store: tradeStore}, Store: tradeStore}
	created, err := handler.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "main", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, SettlementAsset: "USDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo().String())
	require.Empty(t, created.GetLogicalAccount().GetMarkets(), "还没有成员，没有可交易的市场")
	logicalID := created.GetLogicalAccount().GetLogicalAccountId()

	for id, markets := range map[string][]string{"spot-1": {"SPOT"}, "swap-1": {"SWAP"}} {
		account := store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: id, Name: id, Exchange: "BINANCE", Markets: markets,
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
		}
		if markets[0] == "SWAP" {
			account.MarginMode = "CROSS"
		}
		require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
			return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: logicalID, TradingAccountID: id, Enabled: true,
			})
		}))
	}
	got, err := handler.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalID})
	require.NoError(t, err)
	require.Equal(t,
		[]tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT, tradepb.MarketType_MARKET_TYPE_SWAP},
		got.GetLogicalAccount().GetMarkets(), "markets 是启用成员已开通市场的并集")
}
