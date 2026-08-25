package rpc

import (
	"context"
	"path/filepath"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type secretSourceStub struct{}

func (secretSourceStub) GetExchangeSecret(
	context.Context,
	string,
) (accountapp.ExchangeSecret, error) {
	return accountapp.ExchangeSecret{
		SecretID: "secret-1", Category: "exchange",
		Exchange: exchange.ExchangeBinance, Status: "active",
	}, nil
}

func TestAccountRPCRejectsMissingSpace(t *testing.T) {
	response, err := (&AccountServer{}).GetTradingAccount(
		context.Background(),
		&tradepb.GetTradingAccountReq{TradingAccountId: "account-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_NO_PERMISSION {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}

func TestAccountRPCCreateAndSpaceIsolation(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	service := &accountapp.Service{
		Store:   accountapp.Repository{Store: tradeStore},
		Secrets: secretSourceStub{},
	}
	handler := &AccountServer{
		Accounts: service, Store: tradeStore, NewID: func() string { return "account-1" },
	}
	response, err := handler.CreateTradingAccount(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.CreateTradingAccountReq{
			Name: "main", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
			MarketType:      tradepb.MarketType_MARKET_TYPE_SPOT,
			Live:            &tradepb.LiveConfig{Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
			SettlementAsset: "USDT",
			SyncSymbols:     []string{"BTCUSDT"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		response.GetAccount().GetSpaceId() != "space-1" {
		t.Fatalf("create response = %+v", response)
	}
	other, err := handler.GetTradingAccount(
		spacecontext.WithSpaceID(context.Background(), "space-2"),
		&tradepb.GetTradingAccountReq{TradingAccountId: "account-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if other.GetRetInfo().GetCode() != tradepb.ErrorCode_NOT_FOUND ||
		other.GetAccount() != nil {
		t.Fatalf("cross-space response = %+v", other)
	}
}

func TestAccountEnumRoundTrip(t *testing.T) {
	for _, value := range []tradepb.Exchange{
		tradepb.Exchange_EXCHANGE_BINANCE,
		tradepb.Exchange_EXCHANGE_OKX,
	} {
		if got := exchangeToPB(string(exchangeFromPB(value))); got != value {
			t.Fatalf("Exchange round trip = %v, want %v", got, value)
		}
	}
	for _, value := range []tradepb.MarketType{
		tradepb.MarketType_MARKET_TYPE_SPOT,
		tradepb.MarketType_MARKET_TYPE_SWAP,
	} {
		if got := marketToPB(string(marketFromPB(value))); got != value {
			t.Fatalf("market round trip = %v, want %v", got, value)
		}
	}
	for _, value := range []tradepb.AccountEnvironment{
		tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET,
		tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION,
	} {
		if got := environmentToPB(string(environmentFromPB(value))); got != value {
			t.Fatalf("environment round trip = %v, want %v", got, value)
		}
	}
}
