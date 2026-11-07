package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func inspectionHandler(t *testing.T, inspector inspectorStub) (*AccountServer, *store.Store) {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	service := &accountapp.Service{
		Store: accountapp.Repository{Store: tradeStore}, Secrets: secretSourceStub{}, Inspector: inspector,
	}
	return &AccountServer{Accounts: service, Store: tradeStore, NewID: func() string { return "account-1" }}, tradeStore
}

func TestInspectExchangeCredentialDoesNotRequireSpaceAndReturnsInspection(t *testing.T) {
	handler, _ := inspectionHandler(t, inspectorStub{})
	rsp, err := handler.InspectExchangeCredential(context.Background(), &tradepb.InspectExchangeCredentialReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION,
		CredentialSecretId: "secret-1",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, "BINANCE_STANDARD", rsp.GetInspection().GetAccountMode())
	require.Equal(t, []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT, tradepb.MarketType_MARKET_TYPE_SWAP}, rsp.GetInspection().GetAvailableMarkets())
	require.NotZero(t, rsp.GetInspection().GetInspectedAt())
}

func TestInspectExchangeCredentialRejectsInvalidRequest(t *testing.T) {
	handler, _ := inspectionHandler(t, inspectorStub{})
	rsp, err := handler.InspectExchangeCredential(context.Background(), &tradepb.InspectExchangeCredentialReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, CredentialSecretId: "secret-1",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestInspectExchangeCredentialInconclusiveIsNotReportedAsABlocker(t *testing.T) {
	handler, _ := inspectionHandler(t, inspectorStub{err: errors.Join(exchange.ErrInspectionInconclusive, errors.New("timeout"))})
	rsp, err := handler.InspectExchangeCredential(context.Background(), &tradepb.InspectExchangeCredentialReq{
		Exchange: tradepb.Exchange_EXCHANGE_BINANCE, Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION,
		CredentialSecretId: "secret-1",
	})
	require.NoError(t, err)
	require.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "没有完成")
	require.Nil(t, rsp.GetInspection())
}

func TestInspectionToPBKeepsBlockersAndFailureStreak(t *testing.T) {
	value := exchange.Inspection{
		AccountMode: exchange.AccountModeBinancePortfolioMargin, CanWithdraw: true, ConsecutiveFailures: 3, LastFailure: "HTTP 503",
		Blockers: []exchange.InspectionFinding{{Code: exchange.FindingWithdrawPermission, Message: "密钥带有提现权限"}},
	}
	pb := inspectionToPB(value)
	require.True(t, pb.GetCanWithdraw())
	require.Equal(t, int32(3), pb.GetConsecutiveFailures())
	require.Len(t, pb.GetBlockers(), 1)
	require.Equal(t, exchange.FindingWithdrawPermission, pb.GetBlockers()[0].GetCode())
	require.Zero(t, pb.GetInspectedAt(), "零时间不输出检测时间")
}

func TestReinspectTradingAccountWritesResultAndFollowsUp(t *testing.T) {
	handler, tradeStore := inspectionHandler(t, inspectorStub{})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	created, err := handler.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "main", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
		Markets:         []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT},
		Live:            &tradepb.LiveConfig{Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		SettlementAsset: "USDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo().GetMsg())
	require.Equal(t, "BINANCE_STANDARD", created.GetAccount().GetAccountMode(), "建账户时先检测，账户类型由检测写入")
	require.NotEmpty(t, created.GetAccount().GetInspectionJson())

	// 复查发现提现权限：写回结果并交给后续处理（暂停组合、告警）。
	handler.Accounts.Inspector = inspectorStub{result: exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinanceStandard,
		CanWithdraw: true, CanTradeSpot: true, InspectedAt: time.Now().UTC(),
	}}
	var followed accountapp.ReinspectResult
	handler.AfterInspection = func(_ context.Context, spaceID, accountID string, result accountapp.ReinspectResult) error {
		require.Equal(t, "space-1", spaceID)
		require.Equal(t, "account-1", accountID)
		followed = result
		return nil
	}
	rsp, err := handler.ReinspectTradingAccount(ctx, &tradepb.ReinspectTradingAccountReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.True(t, followed.Blocked)
	require.True(t, rsp.GetInspection().GetCanWithdraw())
	stored, err := tradeStore.GetTradingAccount(ctx, "space-1", "account-1")
	require.NoError(t, err)
	inspection, err := exchange.ParseInspection(stored.InspectionJSON)
	require.NoError(t, err)
	require.True(t, inspection.CanWithdraw)

	// 别的空间看不到这个账户。
	other, err := handler.ReinspectTradingAccount(spacecontext.WithSpaceID(context.Background(), "space-2"), &tradepb.ReinspectTradingAccountReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NOT_FOUND, other.GetRetInfo().GetCode())
	missing, err := handler.ReinspectTradingAccount(context.Background(), &tradepb.ReinspectTradingAccountReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NO_PERMISSION, missing.GetRetInfo().GetCode())
}

func TestReinspectTradingAccountInconclusiveKeepsLastResultAndReportsUnavailable(t *testing.T) {
	handler, _ := inspectionHandler(t, inspectorStub{})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	_, err := handler.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "main", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
		Markets:         []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT},
		Live:            &tradepb.LiveConfig{Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		SettlementAsset: "USDT",
	})
	require.NoError(t, err)
	handler.Accounts.Inspector = inspectorStub{err: errors.Join(exchange.ErrInspectionInconclusive, errors.New("HTTP 503"))}
	rsp, err := handler.ReinspectTradingAccount(ctx, &tradepb.ReinspectTradingAccountReq{TradingAccountId: "account-1"})
	require.NoError(t, err)
	require.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int32(1), rsp.GetInspection().GetConsecutiveFailures())
	require.Equal(t, "BINANCE_STANDARD", rsp.GetAccount().GetAccountMode(), "没有结论时保留上次的账户类型")
}

func TestResolveWalletTransferRequiresSpaceAndValidResult(t *testing.T) {
	handler, _ := inspectionHandler(t, inspectorStub{})
	noSpace, err := handler.ResolveWalletTransfer(context.Background(), &tradepb.ResolveWalletTransferReq{TransferId: "t-1", Result: "FAILED"})
	require.NoError(t, err)
	require.NotEqual(t, tradepb.ErrorCode_SUCCESS, noSpace.GetRetInfo().GetCode())

	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	invalid, err := handler.ResolveWalletTransfer(ctx, &tradepb.ResolveWalletTransferReq{TransferId: "t-1", Result: "PENDING"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())

	missing, err := handler.ResolveWalletTransfer(ctx, &tradepb.ResolveWalletTransferReq{TransferId: "t-1", Result: "FAILED"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
}
