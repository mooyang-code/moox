package target

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
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

// reasonTransferScaled 是现货买入被划转额度缩小、钱包没有给出更具体原因时的说明。
const reasonTransferScaled = wallet.ReasonShortfallScaled

// transferPollInterval 是等待划转完成时再次收敛的间隔。
const transferPollInterval = time.Second

// expiryRetryDelay 是到期撤挂单失败后再次收敛的间隔。
const expiryRetryDelay = 5 * time.Second

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
// 真实实现是 application/wallet（通过 NewWalletTransfer 接进来），这里定义执行器要求的行为。
type WalletTransfer interface {
	// BeforeSpotBuy 在现货买入前把资金划进现货钱包，并等现货钱包的快照晚于划转完成时间。
	// buys 只含需要划转的普通账户的现货买入。每一轮收敛都会调用它：实现必须按目标 ID 查划转表保证幂等，
	// 已经划过的不重复划，划转完成但快照还没追上时把该账户放进 Waiting（只挡这个账户的买单）。
	// 合约钱包可划额度不够时，用 BuyScale 把该账户的现货买入按比例缩小（0 到 1，缺省不缩小，0 表示这轮买不了）。
	BeforeSpotBuy(ctx context.Context, spaceID string, target store.LogicalAccountTargetRecord, buys []TransferBuy) (TransferOutcome, error)
	// Collect 在目标执行完毕后把现货钱包多出来的资金归集回合约钱包，pending 为真表示归集还没完成。
	// 同样每一轮都会调用，实现须保证幂等。
	Collect(ctx context.Context, spaceID string, target store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (pending bool, err error)
	// CollectForRisk 是保证金率风控触发后的归集：没有目标（划转记录的 target_id 为空串），一次风控事件由幂等键标识
	// （风控减仓的动作号 margin-<组合>-<时间>）。同一个幂等键最多归集一次，不同幂等键各自归集；每一轮都会调用，实现须幂等。
	CollectForRisk(ctx context.Context, spaceID, logicalAccountID, idempotencyKey string, accounts []store.TradingAccountRecord) (pending bool, err error)
}

// TransferOutcome 是第 4 阶段一次划转检查的结果。
type TransferOutcome struct {
	// Waiting 是本轮还不能买入的账户（键：执行账户 ID）：正在划转、等待现货钱包快照，或失败后在退避等待。
	// 值是给人看的原因，空串表示正常等待、不用显示。只有这些账户的买单被挡，同组合的其他账户照常下单。
	Waiting map[string]string
	// BuyScale 是各账户现货买入要缩小到的比例（键：执行账户 ID，0 到 1）；没有条目表示不缩小。
	BuyScale map[string]shared.Decimal
	// Reasons 是比例缩小或为 0 的原因（键：执行账户 ID），写进 blocked_targets；没有写原因的用默认说明。
	Reasons map[string]string
	// RetryAfter 是建议的下次收敛间隔（失败后退避的剩余时间）；0 表示用 transferPollInterval。
	RetryAfter time.Duration
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
	buys []laneAction,
) (TransferOutcome, error) {
	if e.Transfers == nil {
		return TransferOutcome{}, nil
	}
	var transfers []TransferBuy
	for _, action := range buys {
		if needsWalletTransfer(action.member.account) {
			transfers = append(transfers, TransferBuy{Account: action.member.account, Instrument: action.instrument, Quantity: action.delta.Abs()})
		}
	}
	if len(transfers) == 0 {
		return TransferOutcome{}, nil
	}
	return e.Transfers.BeforeSpotBuy(ctx, spaceID, target, transfers)
}

// scaleBuys 按划转额度缩小现货买入：比例为 0 的账户这轮不买，其余按比例缩小数量。
// 比例和缩小后的数量都向下取到 8 位小数（否则有理数的比例会让 blocked_targets 里的数量写成分数、整轮收敛报错）；
// 缩小后的动作记下原始差额和缩小原因，写 blocked_targets 时用原始差额。
// 约定：比例是“划转实现这一轮看到的可用额度”相对“这一轮计划买入量”的比例。每一轮收敛都会按当前持仓重算计划买入量、
// 再缩一次，已经买到的部分下一轮已在持仓里，不会重复缩小同一笔。实现划转（T9）时要对齐这个口径，比例按本轮算，不要累计。
func scaleBuys(buys []laneAction, scale map[string]shared.Decimal, reasons map[string]string) []laneAction {
	if len(scale) == 0 {
		return buys
	}
	scaled := make([]laneAction, 0, len(buys))
	for _, action := range buys {
		accountID := action.member.account.TradingAccountID
		ratio, found := scale[accountID]
		switch {
		case !found || ratio.Cmp(shared.MustDecimal("1")) >= 0:
			scaled = append(scaled, action)
		default:
			smaller := action.delta.Mul(ratio.RoundDown(8)).RoundDown(8)
			if smaller.Cmp(shared.Zero()) <= 0 {
				continue
			}
			reason := reasons[accountID]
			if reason == "" {
				reason = reasonTransferScaled
			}
			action.fullDelta, action.delta, action.scaled, action.scaleReason = action.delta, smaller, true, reason
			scaled = append(scaled, action)
		}
	}
	return scaled
}

// walletCollect 执行阶段 6；没有配置划转或没有需要归集的账户时直接放行。
// skip 是本目标里还有没做完的现货买单的执行账户：这些账户的资金还要用来买入，不归集。
func (e *Executor) walletCollect(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	members []memberState,
	skip map[string]struct{},
) (bool, error) {
	if e.Transfers == nil {
		return false, nil
	}
	var accounts []store.TradingAccountRecord
	for _, member := range members {
		if _, open := skip[member.account.TradingAccountID]; !open && needsWalletTransfer(member.account) {
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
