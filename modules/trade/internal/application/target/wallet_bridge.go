package target

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// walletBridge 把 application/wallet 的划转服务接成执行器要求的 WalletTransfer：
// wallet 包不依赖 target，类型在这里转换，集成测试和 bootstrap 共用同一份接线。
type walletBridge struct{ service *wallet.Service }

// NewWalletTransfer 返回把划转服务接到执行器的 WalletTransfer。
func NewWalletTransfer(service *wallet.Service) WalletTransfer { return walletBridge{service: service} }

func (b walletBridge) BeforeSpotBuy(ctx context.Context, spaceID string, record store.LogicalAccountTargetRecord, buys []TransferBuy) (TransferOutcome, error) {
	converted := make([]wallet.Buy, 0, len(buys))
	for _, buy := range buys {
		converted = append(converted, wallet.Buy{Account: buy.Account, Instrument: buy.Instrument, Quantity: buy.Quantity})
	}
	outcome, err := b.service.BeforeSpotBuy(ctx, spaceID, record, converted)
	return TransferOutcome{Waiting: outcome.Waiting, BuyScale: outcome.BuyScale, Reasons: outcome.Reasons, RetryAfter: outcome.RetryAfter}, err
}

func (b walletBridge) Collect(ctx context.Context, spaceID string, record store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (bool, error) {
	return b.service.Collect(ctx, spaceID, record, accounts)
}

func (b walletBridge) CollectForRisk(ctx context.Context, spaceID, logicalAccountID, idempotencyKey string, accounts []store.TradingAccountRecord) (bool, error) {
	return b.service.CollectForRisk(ctx, spaceID, logicalAccountID, idempotencyKey, accounts)
}
