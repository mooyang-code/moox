package rpc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func capabilityAccount(mode exchange.ExecutionMode, environment exchange.AccountEnvironment) tradingaccount.Account {
	return tradingaccount.Account{
		ExecutionMode: mode,
		Environment:   environment,
		Status:        exchange.AccountStatusEnabled,
		Ready:         true,
	}
}

func TestResolveExecutionCapabilitiesProductionGate(t *testing.T) {
	got := ResolveExecutionCapabilities(
		capabilityAccount(exchange.ExecutionModeLive, exchange.AccountEnvironmentProduction),
		testDescriptor,
		false,
		true,
	)
	if got.CanPlaceOrder || got.UnavailableReason != "生产交易未开启" {
		t.Fatalf("capabilities = %+v, want production gate", got)
	}
}

func TestResolveExecutionCapabilitiesPaperRequiresMatcher(t *testing.T) {
	got := ResolveExecutionCapabilities(
		capabilityAccount(exchange.ExecutionModePaper, exchange.AccountEnvironmentUnspecified),
		testDescriptor,
		false,
		false,
	)
	if got.CanPlaceOrder || got.UnavailableReason != "模拟撮合未就绪" {
		t.Fatalf("capabilities = %+v, want matcher gate", got)
	}
	ready := ResolveExecutionCapabilities(
		capabilityAccount(exchange.ExecutionModePaper, exchange.AccountEnvironmentUnspecified),
		testDescriptor,
		false,
		true,
	)
	if !ready.CanPlaceOrder || !ready.CanClosePaperSimulation || !ready.Valid() {
		t.Fatalf("ready paper capabilities = %+v", ready)
	}
}

func (c ExecutionCapabilities) Valid() bool {
	return c.CanPlaceOrder || strings.TrimSpace(c.UnavailableReason) != ""
}

var testDescriptor = execution.ExchangeDescriptor{
	OrderTypes:   []exchange.OrderType{exchange.OrderTypeMarket, exchange.OrderTypeLimit},
	FillPolicies: []exchange.FillPolicy{exchange.FillPolicyGTC, exchange.FillPolicyIOC, exchange.FillPolicyFOK},
}

func TestResolveExecutionCapabilitiesNotReadyKeepsOrderTypes(t *testing.T) {
	account := capabilityAccount(exchange.ExecutionModeLive, exchange.AccountEnvironmentTestnet)
	account.Ready = false
	got := ResolveExecutionCapabilities(account, testDescriptor, false, true)
	if got.CanPlaceOrder || len(got.OrderTypes) == 0 || len(got.FillPolicies) == 0 {
		t.Fatalf("capabilities = %+v, want order types without placement", got)
	}
}

func TestGetExecutionCapabilitiesThroughConsoleServer(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "paper", Exchange: "BINANCE", MarketType: "SPOT",
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
		})
	}); err != nil {
		t.Fatal(err)
	}
	registry := execution.NewRegistry()
	registry.Register(execution.ExchangeDescriptor{Exchange: exchange.ExchangeBinance, OrderTypes: testDescriptor.OrderTypes}, func(exchange.AccountConfig, exchange.Credential) (execution.ExecutionAdapter, error) {
		return nil, nil
	})
	server := &ConsoleServer{Store: tradeStore, Exchanges: registry}
	response, err := server.GetExecutionCapabilities(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.GetExecutionCapabilitiesReq{TradingAccountId: "account-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetCapabilities().GetOrderTypes()) == 0 {
		t.Fatalf("response = %+v", response)
	}
}
