package rpc

import (
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ExecutionCapabilities 是服务端计算出的执行账户能力模型，避免前端重复实现生产开关或模拟撮合就绪判断。
type ExecutionCapabilities struct {
	CanPlaceOrder        bool
	UnavailableReason    string
	OrderTypes           []exchange.OrderType
	FillPolicies         []exchange.FillPolicy
	CanClosePaperAccount bool
}

func ResolveExecutionCapabilities(
	account store.TradingAccountRecord,
	descriptor execution.ExchangeDescriptor,
	liveTradingEnabled bool,
	matcherReady bool,
) ExecutionCapabilities {
	capabilities := ExecutionCapabilities{
		OrderTypes:   descriptor.OrderTypes,
		FillPolicies: descriptor.FillPolicies,
		CanClosePaperAccount: account.ExecutionMode == string(exchange.ExecutionModePaper) &&
			account.Status == string(exchange.AccountStatusEnabled),
	}
	if account.Status != string(exchange.AccountStatusEnabled) {
		capabilities.UnavailableReason = "账户已停用"
		return capabilities
	}
	if !account.Ready {
		capabilities.UnavailableReason = "账户未就绪"
		return capabilities
	}
	if account.ExecutionMode == string(exchange.ExecutionModePaper) && !matcherReady {
		capabilities.UnavailableReason = "模拟撮合未就绪"
		return capabilities
	}
	if account.ExecutionMode == string(exchange.ExecutionModeLive) &&
		account.MarketDataEnvironment() == exchange.AccountEnvironmentProduction && !liveTradingEnabled {
		capabilities.UnavailableReason = "生产交易未开启"
		return capabilities
	}
	capabilities.CanPlaceOrder = true
	return capabilities
}
