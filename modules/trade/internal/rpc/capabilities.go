package rpc

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// ExecutionCapabilities is the server-side capability model used by both the
// HTTP console and non-HTTP callers. Keeping the decision here prevents the UI
// from re-implementing the production gate or Paper matcher readiness rules.
type ExecutionCapabilities struct {
	CanPlaceOrder           bool
	UnavailableReason       string
	OrderTypes              []exchange.OrderType
	FillPolicies            []exchange.FillPolicy
	CanClosePaperSimulation bool
}

func ResolveExecutionCapabilities(
	account tradingaccount.Account,
	descriptor execution.VenueDescriptor,
	liveTradingEnabled bool,
	matcherReady bool,
) ExecutionCapabilities {
	capabilities := ExecutionCapabilities{
		OrderTypes:   descriptor.OrderTypes,
		FillPolicies: descriptor.FillPolicies,
		CanClosePaperSimulation: account.ExecutionMode == exchange.ExecutionModePaper &&
			account.Status == exchange.AccountStatusEnabled,
	}
	if account.Status != exchange.AccountStatusEnabled {
		capabilities.UnavailableReason = "trading account is disabled"
		return capabilities
	}
	if !account.Ready {
		capabilities.UnavailableReason = "trading account is not ready"
		return capabilities
	}
	if account.ExecutionMode == exchange.ExecutionModePaper && !matcherReady {
		capabilities.UnavailableReason = "paper matcher is not ready"
		return capabilities
	}
	if account.ExecutionMode == exchange.ExecutionModeLive &&
		account.MarketDataEnvironment() == exchange.AccountEnvironmentProduction && !liveTradingEnabled {
		capabilities.UnavailableReason = "production trading is disabled"
		return capabilities
	}
	capabilities.CanPlaceOrder = true
	return capabilities
}
