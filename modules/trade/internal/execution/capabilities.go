package execution

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 可选能力的名字，配合 CapabilityProbe 使用。
const (
	CapabilityTransfer    = "transfer"
	CapabilityFeeDiscount = "fee_discount"
	CapabilityDust        = "dust"
)

// CapabilityProbe 由实现了某个可选能力接口、但只在部分市场里可用的适配器实现：
// 币安普通账户按市场拆成现货、合约两个适配器，钱包划转、小额兑换只在现货这个适配器里有意义。
// 没有实现 CapabilityProbe 的适配器，实现了接口就视为可用。
type CapabilityProbe interface {
	Supports(capability string) bool
}

// Supports 报告适配器是否具备某个可选能力。
func Supports(adapter any, capability string) bool {
	if probe, ok := adapter.(CapabilityProbe); ok {
		return probe.Supports(capability)
	}
	return true
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

// FeeDiscountConfigurer 是支持用 BNB 抵扣手续费的场所才实现的能力。
// 唯一一个自动修改交易所账户设置的地方：只在用户于资金设置里显式开启抵扣时调用。
type FeeDiscountConfigurer interface {
	// SetBNBFeeDiscount 开关 BNB 抵扣；同一账户的多个市场适配器各设自己负责的那部分（现货与利息、合约）。
	SetBNBFeeDiscount(ctx context.Context, enabled bool) error
	// GetBNBFeeDiscount 报告当前是否开启；适配器负责的各部分全部开启才算开启。
	GetBNBFeeDiscount(ctx context.Context) (bool, error)
}

// DustAsset 是可以兑换成 BNB 的小额资产。
type DustAsset struct {
	Asset string
	// Amount 是可兑换的数量；BNBValue 是预计换得的 BNB 数量。
	Amount   shared.Decimal
	BNBValue shared.Decimal
}

// DustResult 是小额兑换的结果。
type DustResult struct {
	TotalBNB      shared.Decimal
	TransferIDs   []string
	ConvertedList []string
}

// DustConverter 是支持把小额资产兑换成 BNB 的场所才实现的能力。
type DustConverter interface {
	ListDust(ctx context.Context) ([]DustAsset, error)
	ConvertDust(ctx context.Context, assets []string) (DustResult, error)
}
