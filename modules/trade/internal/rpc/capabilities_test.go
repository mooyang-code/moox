package rpc

import (
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
		false,
		false,
	)
	if got.CanPlaceOrder || got.UnavailableReason != "paper matcher is not ready" {
		t.Fatalf("capabilities = %+v, want matcher gate", got)
	}
	ready := ResolveExecutionCapabilities(
		capabilityAccount(exchange.ExecutionModePaper, exchange.AccountEnvironmentUnspecified),
		false,
		true,
	)
	if !ready.CanPlaceOrder || !ready.CanClosePaperSimulation || !ready.Valid() {
		t.Fatalf("ready paper capabilities = %+v", ready)
	}
}
