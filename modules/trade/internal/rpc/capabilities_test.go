package rpc

import (
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"strings"
	"testing"

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
	if got.CanPlaceOrder || got.UnavailableReason != "production trading is disabled" {
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
	if got.CanPlaceOrder || got.UnavailableReason != "paper matcher is not ready" {
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

var testDescriptor = execution.VenueDescriptor{
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
