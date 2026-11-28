package execution

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 可选能力的名字，配合 CapabilityProbe 与 Supports 使用。
const (
	CapabilityTransfer      = "transfer"
	CapabilityFeeDiscount   = "fee_discount"
	CapabilityDust          = "dust"
	CapabilityAccountEvents = "account_events"
	CapabilityFundingRates  = "funding_rates"
	CapabilityLedger        = "ledger"
)

// CapabilityProbe 由实现了某个可选能力接口、但只在部分市场或运行条件下可用的适配器实现：
// 币安普通账户按市场拆成现货、合约两个适配器，钱包划转、小额兑换只在现货适配器可用，
// 资金费率与资金流水只在合约适配器可用；多市场合成适配器则按子适配器能力汇总。
// 没有实现 CapabilityProbe 的适配器，只要实现了对应接口就视为可用。
type CapabilityProbe interface {
	Supports(capability string) bool
}

func implementsCapability(adapter any, capability string) bool {
	if adapter == nil {
		return false
	}
	switch capability {
	case CapabilityTransfer:
		_, ok := adapter.(WalletTransferer)
		return ok
	case CapabilityFeeDiscount:
		_, ok := adapter.(FeeDiscountConfigurer)
		return ok
	case CapabilityDust:
		_, ok := adapter.(DustConverter)
		return ok
	case CapabilityAccountEvents:
		_, ok := adapter.(AccountEventSource)
		return ok
	case CapabilityFundingRates:
		_, ok := adapter.(FundingRateSource)
		return ok
	case CapabilityLedger:
		_, ok := adapter.(LedgerSource)
		return ok
	default:
		return false
	}
}

// Supports 报告适配器是否具备某个可选能力：须先实现对应接口，若实现了 CapabilityProbe 再按探针结果收窄。
func Supports(adapter any, capability string) bool {
	if !implementsCapability(adapter, capability) {
		return false
	}
	if probe, ok := adapter.(CapabilityProbe); ok {
		return probe.Supports(capability)
	}
	return true
}

// LedgerOf 返回适配器真实具备的资金流水来源；不支持时返回 nil。
func LedgerOf(adapter any) LedgerSource {
	if !Supports(adapter, CapabilityLedger) {
		return nil
	}
	return adapter.(LedgerSource)
}

// TransferStatus 是一次钱包划转在交易所侧的状态。
type TransferStatus string

const (
	TransferPending   TransferStatus = "PENDING"
	TransferConfirmed TransferStatus = "CONFIRMED"
	TransferFailed    TransferStatus = "FAILED"
)

// TransferReceipt 是提交划转后交易所返回的凭证。
type TransferReceipt struct {
	ExchangeTransferID string
}

// TransferRecord 是交易所划转记录里的一条。
type TransferRecord struct {
	ExchangeTransferID string
	Asset              string
	Amount             shared.Decimal
	From, To           exchange.Wallet
	Status             TransferStatus
	At                 time.Time
}

// WalletTransferer 是同一账户的钱包之间可以划转资金的场所才实现的能力（币安普通账户的现货、合约钱包）。
// 币安的万向划转接口没有客户端流水号，clientRef 只用于本地对账记录，不会发给交易所；
// 重试前要用 ListTransfers 按时间窗口和金额查交易所的记录，确认没有到账才重试。
type WalletTransferer interface {
	Transfer(ctx context.Context, from, to exchange.Wallet, asset string, amount shared.Decimal, clientRef string) (TransferReceipt, error)
	// ListTransfers 返回 since 之后这个账户在两个钱包之间的划转记录（两个方向）。
	ListTransfers(ctx context.Context, since time.Time) ([]TransferRecord, error)
}
