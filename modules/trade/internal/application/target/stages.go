package target

import (
	"context"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// 执行阶段（阶段 1 撤旧单在 Converge 里先做，阶段 4、6 是普通账户的钱包划转）：
// 2 减仓（现货卖出、合约减仓）→ 3 合约加仓 → 4 划转 → 5 现货买入 → 6 归集。
// 阶段不单独存储，每轮从当前差额推出。
const (
	stageReduce  = 2
	stageSwapAdd = 3
	stageSpotBuy = 5
)

// transferPollInterval 是等待划转完成时再次收敛的间隔。
const transferPollInterval = time.Second

// actionStage 返回一个动作所属的执行阶段。
func actionStage(action laneAction) int {
	switch {
	case action.reducing:
		return stageReduce
	case exchange.RuleFor(action.instrument.MarketType).PositionFromBalance:
		return stageSpotBuy
	default:
		return stageSwapAdd
	}
}

// WalletTransfer 是普通账户第 4、6 阶段的钱包划转。统一账户、OKX、模拟盘没有划转，不会调用它；
// 真实交易所的划转由实盘下单能力的后续任务实现，这里只定义执行器要求的行为。
type WalletTransfer interface {
	// BeforeSpotBuy 在现货买入前把资金划进现货钱包，并等现货钱包的快照晚于划转完成时间。
	// 返回 pending 为真表示还在划转或等待快照，本轮不进入现货买入。members 只含需要划转的普通账户。
	BeforeSpotBuy(ctx context.Context, spaceID string, target store.LogicalAccountTargetRecord, buys []TransferBuy) (pending bool, err error)
	// Collect 在目标执行完毕后把现货钱包多出来的资金归集回合约钱包，pending 为真表示归集还没完成。
	Collect(ctx context.Context, spaceID string, target store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (pending bool, err error)
}

// TransferBuy 是一个需要划转资金的现货买入：账户、标的和计划买入的基础资产数量（金额由划转实现按报价折算）。
type TransferBuy struct {
	Account    store.TradingAccountRecord
	Instrument store.InstrumentRecord
	Quantity   shared.Decimal
}

// needsWalletTransfer 报告执行账户的现货和合约资金是否在不同钱包里，需要在两者之间划转。
func needsWalletTransfer(account store.TradingAccountRecord) bool {
	return account.ExecutionMode == string(exchange.ExecutionModeLive) &&
		account.AccountMode == string(exchange.AccountModeBinanceStandard) &&
		account.HasBalanceMarket() && account.HasPositionMarket()
}

// walletTransferBeforeSpotBuy 在现货买入阶段之前执行阶段 4；没有配置划转或没有需要划转的账户时直接放行。
func (e *Executor) walletTransferBeforeSpotBuy(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	_ []memberState,
	buys []laneAction,
) (bool, error) {
	if e.Transfers == nil {
		return false, nil
	}
	var transfers []TransferBuy
	for _, action := range buys {
		if needsWalletTransfer(action.member.account) {
			transfers = append(transfers, TransferBuy{Account: action.member.account, Instrument: action.instrument, Quantity: action.delta.Abs()})
		}
	}
	if len(transfers) == 0 {
		return false, nil
	}
	return e.Transfers.BeforeSpotBuy(ctx, spaceID, target, transfers)
}

// walletCollect 执行阶段 6；没有配置划转或没有需要归集的账户时直接放行。
func (e *Executor) walletCollect(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	members []memberState,
) (bool, error) {
	if e.Transfers == nil {
		return false, nil
	}
	var accounts []store.TradingAccountRecord
	for _, member := range members {
		if needsWalletTransfer(member.account) {
			accounts = append(accounts, member.account)
		}
	}
	if len(accounts) == 0 {
		return false, nil
	}
	return e.Transfers.Collect(ctx, spaceID, target, accounts)
}

// restingOrder 报告订单是否是挂在簿上的限价 GTC 单：它不阻塞收敛。
func restingOrder(record store.OrderRecord) bool {
	state := orderdomain.State(record.State)
	return record.OrderType == string(exchange.OrderTypeLimit) &&
		record.TimeInForce == string(exchange.TimeInForceGTC) &&
		(state == orderdomain.Open || state == orderdomain.PartiallyFilled)
}

// restingRetryAfter 返回只剩挂单时的唤醒间隔：距目标有效期末的剩余时间，至少 1 秒。
func (e *Executor) restingRetryAfter(target store.LogicalAccountTargetRecord) time.Duration {
	remaining := time.UnixMilli(target.ValidUntil).Sub(e.now())
	if remaining < time.Second {
		return time.Second
	}
	return remaining
}
