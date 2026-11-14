package target

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var (
	ErrExecutorConfig     = errors.New("trade target: executor is not configured")
	ErrInvalidTarget      = errors.New("trade target: invalid target")
	ErrTargetExpired      = errors.New("trade target: target validity window elapsed")
	ErrTargetSession      = errors.New("trade target: target session authorization changed")
	errTargetNotEffective = errors.New("trade target: target is not effective yet")
)

const (
	StatusPending    = "PENDING"
	StatusConverging = "CONVERGING"
	StatusConverged  = "CONVERGED"
	StatusBlocked    = "BLOCKED"
	StatusPaused     = "PAUSED"
	StatusExpired    = "EXPIRED"
	StatusSuperseded = "SUPERSEDED"
)

type OrderService interface {
	Capacity(context.Context, string, orderdomain.OrderSpec) (shared.Decimal, error)
	Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error)
	Submit(context.Context, string, string) (orderdomain.Order, error)
	SubmitBatch(context.Context, string, []string) ([]orderapp.BatchResult, error)
	ResolveUnknownBatch(context.Context, string, []string) ([]orderapp.BatchResult, error)
	Cancel(context.Context, string, string) (orderdomain.Order, error)
	DiscardPending(context.Context, string, string) (orderdomain.Order, error)
	ResolveUnknown(context.Context, string, string) (orderdomain.Order, error)
}

type Executor struct {
	Store     *store.Store
	Orders    OrderService
	Prices    PriceSource
	Exchanges execution.ExchangeCatalog
	Now       func() time.Time
	// OnReversal 在同一目标内第一次出现反向子单时调用，用于告警。
	OnReversal func(spaceID, logicalAccountID, instrumentID string, at time.Time)
	// Observe 记录下单量指标，为空时不记录。
	Observe ExecutionObserver
	// BatchSize 返回一个执行账户每批最多下几单，为空时永续 16、现货 8。
	BatchSize func(marketType string) int
	// MinBatchInterval 是同一组合账户相邻两批的最小间隔，为零时不限制。
	MinBatchInterval time.Duration
	// PaperMinBatchInterval 是成员全部为模拟盘时的批间隔，为零时用 MinBatchInterval。
	// 模拟盘没有交易所限频，间隔只用来让撮合和同步跟上。
	PaperMinBatchInterval time.Duration
	// RateStatus 返回执行账户所在接口的限流状态，模拟盘没有。
	RateStatus func(tradingAccountID string) (exchange.RateStatus, bool)
	// Transfers 执行普通账户第 4、6 阶段的钱包划转，为空时跳过这两个阶段。
	Transfers WalletTransfer

	batchMu   sync.Mutex
	lastBatch map[string]time.Time
	// warned 记录已经告警过的事项（见 warnOnce）。
	warned map[string]struct{}
}

const (
	defaultSwapBatchSize = 16
	defaultSpotBatchSize = 8
	// batchUsageLimit 是停发新批的权重用量比例。
	batchUsageLimit = 0.9
)

// ExecutionObserver 接收收敛过程中的下单量事件。
type ExecutionObserver interface {
	// Blocked 记录被下单前持仓校验或同目标反向熔断跳过的标的。
	Blocked(tradingAccountID, result string)
	// ChildPlaced 记录当前目标又发出一笔子单；crossTargetReversal 表示它和上一个目标在同一标的上的成交方向相反。
	ChildPlaced(logicalAccountID, targetID string, crossTargetReversal bool)
}

const (
	// positionReflectWindow 是下单前持仓校验的回看时长。
	positionReflectWindow     = 10 * time.Minute
	reasonPositionUnreflected = "持仓未反映已成交子单"
	reasonTargetReversal      = "同一目标内来回下单"
	reasonHourlyCapDeferred   = "单币每小时上限已用完，顺延到下一个整点继续"
)

type Result struct {
	Status string
	Action string
	// RetryAfter 非零时表示批间隔未到，过这么久再收敛即可发下一批，不必等定时器。
	RetryAfter time.Duration
}

type memberState struct {
	member      store.LogicalAccountMemberRecord
	account     store.TradingAccountRecord
	instruments map[string]store.InstrumentRecord
	positions   map[string]shared.Decimal
	// sellable 是持仓来自余额的市场可卖出的数量，取自 Available。
	// positions 用 Total，股票当日买入的数量算持仓但不可卖。
	sellable map[string]shared.Decimal
	// t1 是有 T+1 规则的市场（股票）当日买入、当天还不能卖的数量：总数 − 冻结 − 可卖。挂单冻结的部分不在里面。
	t1      map[string]shared.Decimal
	blocked []store.BlockedTarget
	// skip 是执行器不碰的标的（内部标的 ID）：名单里已经不能交易的，或正在做下架清仓的。
	skip map[string]bool
	// reduceOnly 是只减不增的标的：名单里还能交易的。
	reduceOnly map[string]bool
}

// placement 是一次下单尝试的结果。tooSmall 表示全部成员都因最小量或最小金额下不了单。
type placement struct {
	placed   bool
	orderID  string
	reason   string
	tooSmall bool
	// deferred 表示本批这个执行账户的额度用完，留到下一批。
	deferred bool
	// capDeferred 表示单币每小时上限的剩余额度不够下一单，这个标的顺延到下一个整点。
	capDeferred bool
	// viable 只在试算（dryRun）时出现：这一笔单会被接受下单（通过了交易规则、拒单排除、伪市价次数、反向次数等检查），
	// 还没检查的只有资金容量和真正落库。
	viable bool
	// belowConfiguredMin 表示差额低于设置的最小下单额，视为已满足：跳过这个标的，不阻塞，blocked_targets 不记
	// （和调仓阈值同口径；合约减仓、清仓不受这个门槛限制，照常下）。
	belowConfiguredMin bool
	// deferReason 非空表示按市场规则只能顺延（停牌、涨停、跌停、当日买入次日可卖，见 deferralReason）：
	// 写进 blocked_targets 但不算阻塞，不告警。
	deferReason string
}

type laneAction struct {
	member     memberState
	instrument store.InstrumentRecord
	delta      shared.Decimal
	reducing   bool
	// resting 表示这一片改成挂单（限价 + GTC，挂买一、卖一价）；hasResting 表示这个标的已经有挂单，差额里扣过它的剩余量。
	resting    bool
	hasResting bool
	// rested 表示本目标在这个标的上挂过单、挂单已经结束：不再挂单。
	rested bool
	// scaled 表示现货买入被划转额度缩小过：delta 是缩小后的数量，fullDelta 是缩小前的原始差额，
	// scaleReason 是缩小的原因。blocked_targets 记原始差额；缩小后太小下不了单也要记原因，不能当“已满足”悄悄跳过。
	scaled      bool
	fullDelta   shared.Decimal
	scaleReason string
	// noTransfer 表示这笔现货买入不向钱包要资金：这个成员（首选成员）在本目标已经下不了单，
	// 但别的成员能下，下单时会换成员，给它划转没有意义。
	noTransfer bool
}

// blockedQuantity 是写进 blocked_targets 的数量：被缩小过的买入记缩小前的原始差额。
func (a laneAction) blockedQuantity() string {
	if a.scaled {
		return a.fullDelta.String()
	}
	return a.delta.String()
}

func (e *Executor) Converge(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (result Result, resultErr error) {
	if e == nil || e.Store == nil || e.Orders == nil || e.Prices == nil {
		return Result{}, ErrExecutorConfig
	}
	// 等待逻辑账户锁必须响应期限：被占用的账户超时后返回可重试错误，不能卡住后面的账户
	unlock, lockErr := e.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return Result{}, lockErr
	}
	defer unlock()

	logicalAccount, err := e.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	if logicalAccount.ControlMode != "STRATEGY" {
		return Result{}, ErrTargetSession
	}
	target, err := e.Store.GetLogicalAccountTarget(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	// Validity may lapse during quote/prepare as well as before the scan. All
	// expiry exits persist the original target identity, never a replacement.
	defer func() {
		switch {
		case singleCauseIs(resultErr, ErrTargetExpired) || singleCauseIs(resultErr, orderapp.ErrTargetExpired):
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			// 先释放待提交子单再置为过期：释放失败时目标不变，下一轮重试。
			if err := e.releaseExpiredTargetOrders(persistCtx, spaceID, target); err != nil {
				// 撤挂单失败：目标保持原状，过一会儿再来一次，不能等定时器（已收敛、已阻塞的目标不被扫描）。
				result, resultErr = Result{RetryAfter: expiryRetryDelay}, accountExecutionError(err)
				return
			}
			target.Status = StatusExpired
			target.LastError = ""
			updated, err := e.Store.UpdateLogicalAccountTargetState(persistCtx, target)
			if err != nil {
				// 挂单已经撤掉，只是写“已过期”失败：设重试唤醒，已收敛、已阻塞的目标不被定时扫描，不设会一直卡住。
				result, resultErr = Result{RetryAfter: expiryRetryDelay}, err
				return
			}
			result, resultErr = Result{Status: StatusExpired}, nil
			if !updated {
				result.Status = StatusSuperseded
			}
		case singleCauseIs(resultErr, errTargetNotEffective):
			result, resultErr = Result{Status: StatusPaused}, nil
		default:
			resultErr = accountExecutionError(resultErr)
		}
	}()
	if target.Status == StatusExpired {
		return Result{Status: StatusExpired}, nil
	}
	// 过期不是撤单或平仓的理由，新的有效目标到达后才继续收敛。
	if err := e.checkTargetExecutable(ctx, logicalAccount, target); err != nil {
		return Result{Status: StatusPaused}, err
	}
	if logicalAccount.AutomationState != "ACTIVE" {
		return e.stopWhenInactive(ctx, spaceID, logicalAccountID, logicalAccount, &target)
	}
	if target.InstanceID == "" || logicalAccount.OwnerInstanceID != target.InstanceID || logicalAccount.OwnerSessionID != target.SessionID {
		return Result{}, ErrTargetSession
	}
	// 快速路径：只在等当前目标已发出的子单结束时，不加载成员、不逐个标的计算。每笔成交都会唤醒收敛，
	// 这段等待如果每次都走完整计算，会和成交入账抢 SQLite 连接。
	if waiting, err := e.waitingOnChildren(ctx, spaceID, logicalAccountID, target); err != nil {
		return Result{}, err
	} else if waiting {
		if target.Status != StatusConverging {
			if err := e.updateTarget(ctx, &target, StatusConverging); err != nil {
				return Result{}, err
			}
		}
		return Result{Status: StatusConverging, Action: "wait"}, nil
	}

	orderSettings, err := loadOrderSettings(logicalAccount)
	if err != nil {
		// 下单设置读不出来：这个组合不下单，原因写进目标。
		target.LastError = err.Error()
		if updateErr := e.updateTarget(ctx, &target, StatusPending); updateErr != nil {
			return Result{}, updateErr
		}
		return Result{Status: StatusPaused}, nil
	}
	flattens, err := e.runningDelistFlattens(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Result{}, err
	}
	// 时段外（夜里、午休、周末）TargetWorker 每秒都会来：交易时段只看成员账户的场所，先用成员和账户记录判断，
	// 不在时段内就不做完整的成员加载（扫订单、复制标的）。
	if reason, err := e.outOfSessionReason(ctx, logicalAccount); err != nil {
		return Result{}, err
	} else if reason != "" {
		// 状态和原因都没变时不再写库。
		if target.Status == StatusPending && target.LastError == reason {
			return Result{Status: StatusPaused}, nil
		}
		target.LastError = reason
		if err := e.updateTarget(ctx, &target, StatusPending); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusPaused}, nil
	}
	members, err := e.loadMembers(ctx, spaceID, logicalAccountID, flattens)
	if errors.Is(err, store.ErrFundSettingsUnreadable) {
		// 资金设置的排除字段读不出来：这个组合不下单，原因写进目标，不让整个模块显示未就绪。
		target.LastError = err.Error()
		if updateErr := e.updateTarget(ctx, &target, StatusPending); updateErr != nil {
			return Result{}, updateErr
		}
		return Result{Status: StatusPaused}, nil
	}
	if err != nil {
		return Result{}, err
	}
	for _, member := range members {
		if member.account.Status != "ENABLED" || !member.account.Ready {
			target.LastError = "成员 " + member.account.TradingAccountID + " 未就绪"
			if err := e.updateTarget(ctx, &target, StatusPending); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusPaused}, nil
		}
	}

	orders, err := e.activeOrders(ctx, spaceID, logicalAccountID, members)
	if err != nil {
		return Result{}, err
	}
	if early, handled, err := e.releaseConflictsAndStale(ctx, spaceID, logicalAccountID, logicalAccount, &target, orders, flattens); handled || err != nil {
		return early, err
	}
	if blocked := blockedExposures(members); len(blocked) > 0 {
		return e.stopForBlockedExposure(ctx, &target, orders, blocked)
	}

	desired, err := desiredTargets(target)
	if err != nil {
		return Result{}, err
	}
	// 下架名单：名单里的标的只减不增，不能交易的整项跳过；在 reconcileResting 之前截断，名单标的的买入挂单会随之撤掉。
	original := make(map[string]desiredTarget, len(desired))
	for instrumentID, want := range desired {
		original[instrumentID] = want
	}
	truncated := constrainDesired(desired, members)
	env := e.newLaneEnv(ctx, spaceID, logicalAccountID, orderSettings, members, desired)
	e.warnReduceOnly(ctx, env, logicalAccountID, original, truncated, members)
	// 规则：有在途子单（待提交、提交中、结果未知、挂单中）时本轮只处理这些单，不发新批。
	resting, early, handled, err := e.handleTargetChildren(ctx, spaceID, logicalAccount, &target, orders, desired, members, env, orderSettings)
	if handled || err != nil {
		return early, err
	}
	actions, blocked, stop, err := e.planActions(ctx, spaceID, logicalAccount, &target, members, desired, env, resting, orderSettings)
	if err != nil {
		return Result{}, err
	}
	if stop != nil {
		return *stop, nil
	}
	run := &stageRun{blocked: blocked, openBuyAccounts: map[string]struct{}{}}
	if early, handled, err := e.runStages(ctx, spaceID, logicalAccount, &target, members, desired, env, actions, run); handled || err != nil {
		return early, err
	}
	return e.finishConverge(ctx, spaceID, logicalAccount, &target, members, run, resting, orderSettings)
}

// HasDifference 判断目标和实际持仓是否还有能下单的差额，用于过期目标的告警。
func (e *Executor) HasDifference(ctx context.Context, target store.LogicalAccountTargetRecord) (bool, error) {
	flattens, err := e.runningDelistFlattens(ctx, target.SpaceID, target.LogicalAccountID)
	if err != nil {
		return false, err
	}
	members, err := e.loadMembers(ctx, target.SpaceID, target.LogicalAccountID, flattens)
	if err != nil {
		return false, err
	}
	desired, err := desiredTargets(target)
	if err != nil {
		return false, err
	}
	constrainDesired(desired, members)
	logical, err := e.Store.GetLogicalAccount(ctx, target.SpaceID, target.LogicalAccountID)
	if err != nil {
		return false, err
	}
	orderSettings, err := loadOrderSettings(logical)
	if err != nil {
		return false, err
	}
	env := e.newLaneEnv(ctx, target.SpaceID, target.LogicalAccountID, orderSettings, members, desired)
	for _, instrumentID := range laneIDs(desired, members) {
		action, complete, reason, err := e.laneAction(env, instrumentID, desired[instrumentID], members)
		if err != nil {
			return false, err
		}
		if complete {
			continue
		}
		// 差额低于设置的最小下单额的，收敛时视为已满足，这里同口径：不算还有差额，免得误报“目标过期且持仓未到位”。
		if reason == "" && e.belowConfiguredMinimumNow(ctx, env, action) {
			continue
		}
		// 顺延（停牌、涨跌停、T+1）和整手市场里不足一手的差额，收敛时同样不算未到位。
		if reason == "" && e.settledByMarketRule(ctx, action, desired[instrumentID], members) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// settledByMarketRule 判断一条标的剩下的差额是不是按市场规则顺延或视为已满足，和 Converge 下单时的判断同口径：
// 停牌、涨跌停、只因 T+1 卖不出（deferralReason），以及整手交易的市场里不足一手的差额。取不到报价时按“没满足”处理。
func (e *Executor) settledByMarketRule(ctx context.Context, action laneAction, desired desiredTarget, members []memberState) bool {
	// 先按市场规则判断：没有涨跌停、T+1、整手规则的市场（加密货币）不多取一次价。
	if rule := exchange.RuleFor(action.instrument.MarketType); !rule.PriceLimits && !rule.T1 && !rule.BoardLot {
		return false
	}
	quote, err := e.Prices.LatestPrice(ctx, action.member.account.TradingAccountID,
		exchange.NewSymbolKey(action.instrument.MarketType, action.instrument.ExchangeSymbol))
	if err != nil || quote.Price.Cmp(shared.Zero()) <= 0 {
		return false
	}
	if deferralReason(action, quote) != "" {
		return true
	}
	if !exchange.RuleFor(action.instrument.MarketType).BoardLot {
		return false
	}
	_, tooSmall, err := childQuantity(action)
	return err == nil && tooSmall != "" && laneFinished(desired, members, action.instrument.InstrumentID)
}

// spotBuyVerdict 是 spotBuyPrecheck 的结论。
type spotBuyVerdict struct {
	// satisfied：视为已满足，不下单、不划转。
	satisfied bool
	// blockedReason 非空：因为非资金原因注定下不了单，直接记 BLOCKED，不划转。
	blockedReason string
	// capDeferred：单币每小时上限已经用完，这个标的顺延到下一个整点；这个小时一笔都下不了，不划转。
	capDeferred bool
	// noTransfer：首选成员下不了单、别的成员能下：照常进入下单，只是不为首选成员划转。
	noTransfer bool
}

// spotBuyPrecheck 在向钱包要资金之前，按缩小前的原始差额判断一笔现货买入还要不要划转：
//   - 视为已满足（低于设置的最小下单额，或交易规则下下不了单且已有同向持仓的零头），不下单、不划转；
//   - 因为非资金原因注定下不了单（交易规则、要划转的这个账户在本目标已被拒单、反向次数已满、伪市价次数用满等），
//     直接记 BLOCKED，不划转：为一笔注定下不了的单划转，会先划进来、再被归集回去、再划进来，最后钱停在现货钱包；
//   - 每小时上限用完了：顺延到下一个整点，不划转（划进去这个小时也买不了，钱会一直停在现货钱包）；
//   - 都不是：交给钱包算资金需求。
//
// 试算先看全部成员、再看要划转的这个账户（action.member）：这个账户自己下不了单、别的成员能下，不为它划转，下单照常。
// 取不到报价等账户级问题按“没满足”交给后面的流程处理；目标已失效、过期这类目标级错误原样返回。
func (e *Executor) spotBuyPrecheck(
	ctx context.Context,
	env *laneEnv,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	action laneAction,
	desired map[string]desiredTarget,
	members []memberState,
) (spotBuyVerdict, error) {
	quote, err := e.Prices.LatestPrice(ctx, action.member.account.TradingAccountID,
		exchange.NewSymbolKey(action.instrument.MarketType, action.instrument.ExchangeSymbol))
	if err != nil {
		if paper.IsInfrastructureError(err) {
			return spotBuyVerdict{}, err
		}
		return spotBuyVerdict{}, nil
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 {
		return spotBuyVerdict{}, nil
	}
	quantity := floorToStep(action.delta.Abs(), mustBaseStep(action.instrument))
	if env.belowConfiguredMinimum(action, quantity, quote.Price) {
		return spotBuyVerdict{satisfied: true}, nil
	}
	instrumentID := action.instrument.InstrumentID
	finished := laneFinished(desired[instrumentID], members, instrumentID)
	if finished && env.tooSmallToPlace(action, quantity, quote.Price) {
		return spotBuyVerdict{satisfied: true}, nil
	}
	// 试算：先看全部成员（下单时会换成员），再看要划转的这个账户。
	probe, err := e.dryRunPlacement(ctx, spaceID, target, action, members, env)
	if err != nil {
		return spotBuyVerdict{}, err
	}
	switch {
	case probe.capDeferred:
		return spotBuyVerdict{capDeferred: true}, nil
	case probe.viable || probe.deferred || probe.placed:
		if len(members) > 1 {
			// 有成员能下单：再只看要划转的这个账户。它自己下不了、别的成员能下，就不为它划转，下单照常。
			own, ownErr := e.dryRunPlacement(ctx, spaceID, target, action, []memberState{action.member}, env)
			if ownErr != nil {
				return spotBuyVerdict{}, ownErr
			}
			if !(own.viable || own.deferred || own.placed || own.capDeferred) {
				return spotBuyVerdict{noTransfer: true}, nil
			}
		}
		return spotBuyVerdict{}, nil
	case probe.belowConfiguredMin, probe.tooSmall && finished:
		return spotBuyVerdict{satisfied: true}, nil
	}
	return spotBuyVerdict{blockedReason: probe.reason}, nil
}

// dryRunPlacement 试算：只有账户级的问题（取不到报价等）交给后面的流程（按“能下”处理）；
// 目标过期、会话变化、还没生效这类目标级错误原样返回，不先划转。
func (e *Executor) dryRunPlacement(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	action laneAction,
	members []memberState,
	env *laneEnv,
) (placement, error) {
	probe, err := e.placeActionMode(ctx, spaceID, target, action, members, nil, env, true)
	if err != nil {
		var accountErr *AccountError
		if errors.As(err, &accountErr) {
			return placement{viable: true}, nil
		}
		return placement{}, err
	}
	return probe, nil
}

// belowConfiguredMinimumNow 判断一个动作的差额（按步长取整后）是否低于设置的最小下单额；取不到报价时按“不低于”处理。
func (e *Executor) belowConfiguredMinimumNow(ctx context.Context, env *laneEnv, action laneAction) bool {
	quote, err := e.Prices.LatestPrice(ctx, action.member.account.TradingAccountID,
		exchange.NewSymbolKey(action.instrument.MarketType, action.instrument.ExchangeSymbol))
	if err != nil || quote.Price.Cmp(shared.Zero()) <= 0 {
		return false
	}
	quantity := floorToStep(action.delta.Abs(), mustBaseStep(action.instrument))
	return env.belowConfiguredMinimum(action, quantity, quote.Price)
}

// outOfSessionReason 只读启用成员和它们的账户记录，返回第一个不在交易时段的成员的原因，都在时段内时返回空串。
// 这个检查原先排在完整加载成员之后，几个原因的先后照旧：资金设置的排除字段读不出来、有成员停用或未就绪、没有启用成员时
// 都返回空串，由后面的完整路径照原样写原因或报错。
func (e *Executor) outOfSessionReason(ctx context.Context, logical store.LogicalAccountRecord) (string, error) {
	if _, err := e.Store.ResolveFundSettings(logical); err != nil {
		return "", nil
	}
	records, err := e.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return "", err
	}
	accounts := make([]store.TradingAccountRecord, 0, len(records))
	for _, record := range records {
		account, err := e.Store.GetTradingAccountByID(ctx, record.TradingAccountID)
		if err != nil {
			return "", err
		}
		if account.Status != "ENABLED" || !account.Ready {
			return "", nil
		}
		accounts = append(accounts, account)
	}
	now := e.now()
	for _, account := range accounts {
		if ok, reason := execution.CanOrder(e.Exchanges, account.Exchange, now); !ok {
			return reason, nil
		}
	}
	return "", nil
}

func (e *Executor) loadMembers(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	flattens delistFlattens,
) ([]memberState, error) {
	records, err := e.Store.ListLogicalAccountMembers(
		ctx, spaceID, logicalAccountID, false,
	)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: logical account has no enabled members", ErrInvalidTarget)
	}
	logical, err := e.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return nil, err
	}
	// 数值字段坏了按默认处理（告警限频），排除字段坏了直接报错、不下单：排除资产（底仓对冲腿等）不能因为设置坏了被交易。
	settings, err := e.Store.ResolveFundSettings(logical)
	if err != nil {
		return nil, err
	}
	// 下架名单只在这里读一次：成员的“只减不增”、跳过标的、阻塞判断都用同一份。
	blockedSet, err := e.Store.LoadBlockedSet(ctx)
	if err != nil {
		return nil, err
	}
	members := make([]memberState, 0, len(records))
	for _, record := range records {
		account, err := e.Store.GetTradingAccountByID(ctx, record.TradingAccountID)
		if err != nil {
			return nil, err
		}
		instrumentRecords, err := e.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return nil, err
		}
		instruments := make(map[string]store.InstrumentRecord)
		// 现货和合约的交易所代码相同（BTCUSDT），按（市场，代码）找标的。
		symbols := make(map[exchange.SymbolKey]string)
		for _, instrument := range instrumentRecords {
			if !exchange.StatusTradable(instrument.Status) {
				continue
			}
			if instrument.SettlementAsset != "" &&
				instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			instruments[instrument.InstrumentID] = instrument
			symbols[exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)] = instrument.InstrumentID
		}
		skip := make(map[string]bool)
		reduceOnly := make(map[string]bool)
		for _, instrument := range instrumentRecords {
			if blockedSet.ContainsInstrument(account.Exchange, instrument) {
				if _, tradable := instruments[instrument.InstrumentID]; tradable {
					reduceOnly[instrument.InstrumentID] = true
				} else {
					skip[instrument.InstrumentID] = true
				}
			}
			if flattens.covers(account, instrument.InstrumentID) {
				skip[instrument.InstrumentID] = true
			}
		}
		blockedAssets := blockedSet.SpotBaseAssets(account.Exchange, instrumentRecords)
		positions := make(map[string]shared.Decimal)
		sellable := make(map[string]shared.Decimal)
		t1 := make(map[string]shared.Decimal)
		blocked := make([]store.BlockedTarget, 0)
		if account.HasBalanceMarket() {
			managed, managedErr := e.Store.ManagedAssets(ctx, spaceID, account.TradingAccountID)
			if managedErr != nil {
				return nil, managedErr
			}
			for _, balance := range account.SpotBalances() {
				if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
					continue
				}
				quantity, parseErr := shared.ParseDecimal(balance.Total)
				if parseErr != nil {
					return nil, parseErr
				}
				if quantity.Cmp(shared.Zero()) <= 0 {
					continue
				}
				instrumentID, mapped := spotInstrumentID(
					balance.Asset,
					instruments,
				)
				if !mapped {
					// 名单里已经不能交易的现货标的：下架后剩下的余额不阻塞组合，整项跳过。
					if _, delisted := blockedAssets[balance.Asset]; delisted {
						continue
					}
					blocked = append(blocked, store.BlockedTarget{
						InstrumentID: balance.Asset,
						Quantity:     quantity.String(),
						Reason: account.TradingAccountID +
							"：资产没有唯一可交易的标的",
					})
					continue
				}
				if skip[instrumentID] {
					continue
				}
				positions[instrumentID] = positions[instrumentID].Add(quantity)
				available, parseErr := shared.ParseDecimal(balance.Available)
				if parseErr != nil {
					return nil, parseErr
				}
				sellable[instrumentID] = sellable[instrumentID].Add(available)
				if exchange.RuleFor(instruments[instrumentID].MarketType).T1 {
					locked, lockedErr := shared.ParseDecimal(balance.Locked)
					if lockedErr != nil {
						locked = shared.Zero()
					}
					if bought := quantity.Sub(locked).Sub(available); bought.Cmp(shared.Zero()) > 0 {
						t1[instrumentID] = t1[instrumentID].Add(bought)
					}
				}
			}
		}
		if account.HasPositionMarket() {
			positionRecords, listErr := e.Store.ListPositions(
				ctx, spaceID, account.TradingAccountID, exchange.SymbolKey{},
			)
			if listErr != nil {
				return nil, listErr
			}
			for _, position := range positionRecords {
				quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
				if parseErr != nil {
					return nil, parseErr
				}
				if quantity.IsZero() {
					continue
				}
				instrumentID := symbols[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]
				// 套利底仓的对冲腿不参与策略持仓：不在目标里也不能按 0 平掉。
				if instrument, known := instruments[instrumentID]; known && settings.IsArbitrageBase(instrument.BaseAsset) {
					continue
				}
				if instrumentID == "" && blockedSet.Contains(account.Exchange, position.MarketType, position.ExchangeSymbol) {
					continue // 名单里已经不能交易的永续：不阻塞组合，整项跳过
				}
				if instrumentID == "" {
					blocked = append(blocked, store.BlockedTarget{
						InstrumentID: position.ExchangeSymbol,
						Quantity:     quantity.String(),
						Reason: account.TradingAccountID +
							"：持仓没有对应的可交易标的",
					})
					continue
				}
				if skip[instrumentID] {
					continue
				}
				positions[instrumentID] = positions[instrumentID].Add(quantity)
			}
		}
		members = append(members, memberState{
			member: record, account: account,
			instruments: instruments, positions: positions, sellable: sellable, t1: t1, blocked: blocked,
			skip: skip, reduceOnly: reduceOnly,
		})
	}
	return members, nil
}

func spotInstrumentID(
	asset string,
	instruments map[string]store.InstrumentRecord,
) (string, bool) {
	instrumentID := ""
	for candidateID, instrument := range instruments {
		if instrument.BaseAsset != asset || !exchange.RuleFor(instrument.MarketType).PositionFromBalance {
			continue
		}
		if instrumentID != "" && instrumentID != candidateID {
			return "", false
		}
		instrumentID = candidateID
	}
	return instrumentID, instrumentID != ""
}

func blockedExposures(members []memberState) []store.BlockedTarget {
	var blocked []store.BlockedTarget
	for _, member := range members {
		blocked = append(blocked, member.blocked...)
	}
	return blocked
}

// activeOrders 列出组合账户的未结束订单：按组合账户查（停用成员上的在途子单也要算），
// 再加上启用成员账户上的订单（外部订单可能没有组合账户归属）。
func (e *Executor) activeOrders(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	members []memberState,
) ([]store.OrderRecord, error) {
	var active []store.OrderRecord
	seen := make(map[string]struct{})
	add := func(record store.OrderRecord) {
		if _, ok := seen[record.OrderID]; ok || orderdomain.State(record.State).Terminal() {
			return
		}
		seen[record.OrderID] = struct{}{}
		active = append(active, record)
	}
	owned, _, err := e.Store.ListOrders(ctx, spaceID, store.OrderQuery{LogicalAccountID: logicalAccountID, OnlyOpen: true, Limit: 1000})
	if err != nil {
		return nil, err
	}
	for _, record := range owned {
		add(record)
	}
	for _, member := range members {
		records, err := e.Store.ListActiveOrdersForAccount(ctx, spaceID, member.account.TradingAccountID)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			add(record)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].OrderID < active[j].OrderID
	})
	return active, nil
}

type desiredTarget struct {
	Quantity shared.Decimal
	// Hold 为 true 时该标的保持当前持仓数量不动，nextLaneAction 视为已完成。
	Hold bool
}

func desiredTargets(
	target store.LogicalAccountTargetRecord,
) (map[string]desiredTarget, error) {
	desired := make(map[string]desiredTarget, len(target.Targets))
	for _, current := range target.Targets {
		if current.Hold {
			desired[current.InstrumentID] = desiredTarget{Hold: true}
			continue
		}
		quantity, err := shared.ParseDecimal(current.Quantity)
		if err != nil {
			return nil, err
		}
		desired[current.InstrumentID] = desiredTarget{
			Quantity: quantity,
		}
	}
	return desired, nil
}

func laneIDs(
	desired map[string]desiredTarget,
	members []memberState,
) []string {
	set := make(map[string]struct{}, len(desired))
	for instrumentID := range desired {
		set[instrumentID] = struct{}{}
	}
	for _, member := range members {
		for instrumentID, quantity := range member.positions {
			if !quantity.IsZero() {
				set[instrumentID] = struct{}{}
			}
		}
	}
	values := make([]string, 0, len(set))
	for instrumentID := range set {
		values = append(values, instrumentID)
	}
	sort.Strings(values)
	return values
}

// laneAction 先按调仓阈值判断整条标的是否已到位，再算下一步动作。收敛、过期告警和待提交子单的重新判断共用，结论一致。
func (e *Executor) laneAction(
	env *laneEnv,
	instrumentID string,
	desired desiredTarget,
	members []memberState,
) (laneAction, bool, string, error) {
	if !desired.Hold {
		confirmed := shared.Zero()
		for _, member := range members {
			confirmed = confirmed.Add(member.positions[instrumentID])
		}
		if env.withinRebalanceThreshold(desired.Quantity, confirmed, members, instrumentID) {
			return laneAction{}, true, "", nil
		}
	}
	return nextLaneAction(instrumentID, desired, members)
}

func nextLaneAction(
	instrumentID string,
	desired desiredTarget,
	members []memberState,
) (laneAction, bool, string, error) {
	// 保持标的：既不加仓也不减仓。
	if desired.Hold {
		return laneAction{}, true, "", nil
	}
	confirmed := shared.Zero()
	for _, member := range members {
		confirmed = confirmed.Add(member.positions[instrumentID])
	}

	for _, member := range positionsByAbsoluteSize(members, instrumentID) {
		position := member.positions[instrumentID]
		if position.IsZero() {
			continue
		}
		if desired.Quantity.IsZero() || !sameSign(position, desired.Quantity) {
			instrument, ok := member.instruments[instrumentID]
			if !ok {
				return laneAction{}, false,
					"持仓没有对应的可交易标的", nil
			}
			// 反向零头下不了单，跳过这个成员继续判断；整手市场（A 股）还有可卖的零股就一次卖掉。
			if belowMinimumQuantity(position, instrument) &&
				!oddLotFullSell(laneAction{member: member, instrument: instrument, delta: position.Neg(), reducing: true}) {
				continue
			}
			return laneAction{
				member: member, instrument: instrument,
				delta: position.Neg(), reducing: true,
			}, false, "", nil
		}
	}
	if desired.Quantity.IsZero() || confirmed.Cmp(desired.Quantity) == 0 {
		return laneAction{}, true, "", nil
	}
	delta := desired.Quantity.Sub(confirmed)
	if sameSign(confirmed, desired.Quantity) && confirmed.Abs().Cmp(desired.Quantity.Abs()) > 0 {
		excess := confirmed.Abs().Sub(desired.Quantity.Abs())
		for _, member := range positionsByAbsoluteSize(members, instrumentID) {
			position := member.positions[instrumentID]
			if !sameSign(position, desired.Quantity) {
				continue
			}
			quantity := excess
			if position.Abs().Cmp(quantity) < 0 {
				quantity = position.Abs()
			}
			if position.Cmp(shared.Zero()) > 0 {
				quantity = quantity.Neg()
			}
			instrument, ok := member.instruments[instrumentID]
			if !ok {
				return laneAction{}, false,
					"持仓没有对应的可交易标的", nil
			}
			return laneAction{
				member: member, instrument: instrument,
				delta: quantity, reducing: true,
			}, false, "", nil
		}
	}
	for _, member := range members {
		if member.restricted(instrumentID) {
			continue // 只减不增或不碰的成员不能加仓
		}
		if instrument, ok := member.instruments[instrumentID]; ok {
			return laneAction{
				member: member, instrument: instrument,
				delta: delta,
			}, false, "", nil
		}
	}
	return laneAction{}, false, "没有启用的成员支持该标的", nil
}

// belowMinimumQuantity 判断数量按步长取整后是否下不了单。
func belowMinimumQuantity(quantity shared.Decimal, instrument store.InstrumentRecord) bool {
	step, minimum, err := baseQuantityRules(instrument)
	if err != nil {
		return false
	}
	floored := floorToStep(quantity.Abs(), step)
	return floored.IsZero() || floored.Cmp(minimum) < 0
}

// laneFinished 判断下不了单的剩余量能否算完成：目标为 0 或已有同向持仓时是零头，
// 从空仓开新仓就达不到最小量才需要人看（加密货币）；整手交易的市场不足一手一律视为已满足。
func laneFinished(desired desiredTarget, members []memberState, instrumentID string) bool {
	// 整手交易的市场（A 股）：买入不足一手的差额视为已满足，开新仓也一样。
	if desired.Quantity.IsZero() || boardLotLane(members, instrumentID) {
		return true
	}
	confirmed := shared.Zero()
	for _, member := range members {
		confirmed = confirmed.Add(member.positions[instrumentID])
	}
	return !confirmed.IsZero() && sameSign(confirmed, desired.Quantity)
}

func positionsByAbsoluteSize(
	members []memberState,
	instrumentID string,
) []memberState {
	values := append([]memberState(nil), members...)
	sort.SliceStable(values, func(i, j int) bool {
		left := values[i].positions[instrumentID].Abs()
		right := values[j].positions[instrumentID].Abs()
		cmp := left.Cmp(right)
		if cmp != 0 {
			return cmp > 0
		}
		if values[i].member.Priority != values[j].member.Priority {
			return values[i].member.Priority < values[j].member.Priority
		}
		return values[i].account.TradingAccountID <
			values[j].account.TradingAccountID
	})
	return values
}

// placeActionMode 给一笔动作下单；dryRun 为真只试算：走完交易规则、拒单排除、伪市价次数、反向次数等所有“下不了单”的检查，
// 不看批额度、不查资金容量、不落库，能下就返回 viable。现货买入在向钱包要资金之前用它排除下不了的单：
// 为一笔注定下不了的单划转资金，会先划进来、再被归集回去、再划进来。
func (e *Executor) placeActionMode(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	action laneAction,
	members []memberState,
	quota map[string]int,
	env *laneEnv,
	dryRun bool,
) (placement, error) {
	rejected, err := e.Store.RejectedTargetAccounts(ctx, spaceID, target.LogicalAccountID, target.TargetID, action.instrument.InstrumentID)
	if err != nil {
		return placement{}, err
	}
	var tally candidateTally
	candidates := placementCandidates(action, members)
	for _, candidate := range candidates {
		check, out, done, err := e.checkCandidate(ctx, candidate, env, rejected, quota, dryRun, &tally)
		if done || err != nil {
			return out, err
		}
		if check.skip {
			continue
		}
		out, done, err = e.submitCandidate(ctx, spaceID, target, candidate, check, env, quota, dryRun, &tally)
		if done || err != nil {
			return out, err
		}
	}
	result := placement{
		tooSmall:           len(candidates) > 0 && tally.tooSmall == len(candidates),
		belowConfiguredMin: tally.configuredSmall > 0 && tally.tooSmall == len(candidates),
	}
	if tally.t1Deferred && len(tally.capacityErrors) == 0 {
		result.deferReason = reasonT1
		return result, nil
	}
	if len(tally.capacityErrors) == 0 {
		result.reason = "数量低于交易所最小值"
		return result, nil
	}
	result.reason = "成员容量不足：" + strings.Join(tally.capacityErrors, "; ")
	if tally.unfilled {
		result.reason = "未到位：" + strings.Join(tally.capacityErrors, "; ")
	}
	return result, nil
}

// candidateTally 汇总各成员下不了单的情况，全部成员都没下成时用来给出原因。
type candidateTally struct {
	capacityErrors []string
	// tooSmall 只统计交易规则挡住的成员，不受拒单过滤影响：有成员本来能下单但被排除时不能算完成。
	tooSmall int
	// configuredSmall 统计因为低于设置的最小下单额而下不了单的成员。
	configuredSmall int
	// unfilled 表示有成员的伪市价单用满了尝试次数。
	unfilled bool
	// t1Deferred 表示有成员只因为 T+1 卖不出。
	t1Deferred bool
}

// candidateCheck 是一个成员通过交易规则检查后的结果：报价、取整后的数量；skip 表示这个成员下不了单，换下一个。
type candidateCheck struct {
	skip     bool
	quote    Quote
	quantity shared.Decimal
	// oddFull：整手市场卖出全部可卖数量，零股一次卖完，不受最小数量限制。
	oddFull bool
}

// checkCandidate 检查一个候选成员能不能下这一笔：取整、报价、停牌涨跌停、每小时上限、最小名义金额、最小下单额、拒单排除、T+1 可卖量。
// 下不了的原因记进 tally；done 为真表示整笔动作已有结论（out），不再看后面的成员。
func (e *Executor) checkCandidate(
	ctx context.Context,
	candidate laneAction,
	env *laneEnv,
	rejected map[string]string,
	quota map[string]int,
	dryRun bool,
	tally *candidateTally,
) (candidateCheck, placement, bool, error) {
	accountID := candidate.member.account.TradingAccountID
	// 额度按执行账户计：用完算本轮跳过，不换到下一个成员，也不写阻塞原因。
	if !dryRun && quota[accountID] <= 0 {
		return candidateCheck{}, placement{deferred: true}, true, nil
	}
	quantity, reason, err := childQuantity(candidate)
	if err != nil {
		return candidateCheck{}, placement{}, true, err
	}
	if reason != "" {
		tally.capacityErrors = append(tally.capacityErrors, reason)
		tally.tooSmall++
		return candidateCheck{skip: true}, placement{}, false, nil
	}
	quote, err := e.Prices.LatestPrice(
		ctx,
		candidate.member.account.TradingAccountID,
		exchange.NewSymbolKey(candidate.instrument.MarketType, candidate.instrument.ExchangeSymbol),
	)
	if err != nil {
		if paper.IsInfrastructureError(err) {
			return candidateCheck{}, placement{}, true, err
		}
		return candidateCheck{}, placement{}, true, &AccountError{TradingAccountID: candidate.member.account.TradingAccountID, Err: err}
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 {
		return candidateCheck{}, placement{}, true, &AccountError{TradingAccountID: candidate.member.account.TradingAccountID, Err: orderdomain.ErrInvalidSpec}
	}
	// 停牌、涨停时买、跌停时卖：整个标的顺延，不换成员（停牌和涨跌停是标的本身的状态）。
	if exchange.RuleFor(candidate.instrument.MarketType).PriceLimits {
		if reason := deferralReason(laneAction{instrument: candidate.instrument, delta: candidate.delta}, quote); reason != "" {
			return candidateCheck{}, placement{deferReason: reason}, true, nil
		}
	}
	// 最小名义金额：交易所给了就用它，没给（OKX）用 minSz × 合约面值 × 价格折算。
	minNotional, err := candidate.instrument.MinNotionalAt(quote.Price)
	if err != nil {
		return candidateCheck{}, placement{}, true, fmt.Errorf("%w: minimum notional", ErrInvalidTarget)
	}
	// 单币每小时上限：额度不够整笔时只下剩下的；剩下的不够下一单就顺延到下一个整点，不算下不了单。
	quantity, clipped, capErr := env.applyHourlyCap(ctx, candidate, quantity, quote.Price)
	if capErr != nil {
		return candidateCheck{}, placement{}, true, capErr
	}
	if clipped && env.tooSmallToPlace(candidate, quantity, quote.Price) {
		return candidateCheck{}, placement{capDeferred: true}, true, nil
	}
	belowMinimum, err := belowMinimumNotional(quantity, quote.Price, minNotional)
	if err != nil {
		return candidateCheck{}, placement{}, true, err
	}
	// 合约全平只查最小数量，否则剩余仓位永远平不掉。
	if belowMinimum && !isFullClose(candidate, quantity) {
		tally.capacityErrors = append(tally.capacityErrors, accountID+": 金额低于交易所最小名义金额")
		tally.tooSmall++
		return candidateCheck{skip: true}, placement{}, false, nil
	}
	// 配置的最小下单额：合约减仓、清仓例外（否则剩余仓位下不了单、永远平不掉）。
	if env.belowConfiguredMinimum(candidate, quantity, quote.Price) {
		tally.capacityErrors = append(tally.capacityErrors, accountID+": 金额低于配置的最小下单额")
		tally.tooSmall++
		tally.configuredSmall++
		return candidateCheck{skip: true}, placement{}, false, nil
	}
	if reason, excluded := rejected[accountID]; excluded {
		// 同一目标不再向拒过单的账户下单，下一个目标自然重试。
		tally.capacityErrors = append(tally.capacityErrors, accountID+": "+reason)
		return candidateCheck{skip: true}, placement{}, false, nil
	}
	// oddFull：整手市场卖出全部可卖数量，零股一次卖完，不受最小数量限制。
	oddFull := false
	if candidate.reducing && exchange.RuleFor(candidate.instrument.MarketType).PositionFromBalance {
		quantity, oddFull = sellQuantity(candidate, quantity)
		// 截断到可卖量后数量可能低于最小值或最小名义金额，交易所会拒单。
		tooSmallNotional, notionalErr := belowMinimumNotional(quantity, quote.Price, minNotional)
		if notionalErr != nil {
			return candidateCheck{}, placement{}, true, notionalErr
		}
		if quantity.IsZero() || tooSmallNotional {
			if t1Locked(candidate) {
				// 只因为 T+1 卖不出：顺延到次日，不算阻塞。
				tally.t1Deferred = true
				return candidateCheck{skip: true}, placement{}, false, nil
			}
			tally.capacityErrors = append(tally.capacityErrors, accountID+": 可卖量不足")
			return candidateCheck{skip: true}, placement{}, false, nil
		}
	}
	return candidateCheck{quote: quote, quantity: quantity, oddFull: oddFull}, placement{}, false, nil
}

// submitCandidate 给通过检查的候选成员下单：单笔上限、伪市价次数、挂单价、反向次数、资金容量，最后落库。
// 只落库，批量提交由收敛统一发出；done 为假表示这个成员下不了，换下一个成员。
func (e *Executor) submitCandidate(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	candidate laneAction,
	check candidateCheck,
	env *laneEnv,
	quota map[string]int,
	dryRun bool,
	tally *candidateTally,
) (placement, bool, error) {
	accountID := candidate.member.account.TradingAccountID
	quote, quantity, oddFull := check.quote, check.quantity, check.oddFull
	if target.InstanceID != "" {
		if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{
			SpaceID: spaceID, LogicalAccountID: target.LogicalAccountID,
			OwnerInstanceID: target.InstanceID, OwnerSessionID: target.SessionID,
		}, target); err != nil {
			return placement{}, true, err
		}
	}
	// 单笔上限：拆单时每笔的名义金额不超过设置的上限；开了随机化，每笔取上限的 50%～100%。
	// 同一目标、同一标的已发出的子单（所有执行账户），按提交先后：伪市价单的尝试次数按标的统计（换成员不重新计数），
	// 拆单随机化和反向熔断按候选账户统计（见 childrenOfAccount），共用这一次查询。
	children, childrenErr := e.Store.TargetChildOrders(ctx, spaceID, target.LogicalAccountID, target.TargetID, candidate.instrument.InstrumentID)
	if childrenErr != nil {
		return placement{}, true, childrenErr
	}
	// 伪市价单：用满尝试次数仍没成交完的标的记为“未到位”，告警一次，不再重试，等下一份目标重算。
	attempt := 0
	// 挂单价先定：取不到买一卖一就不挂单，这一片走原来的下单方式（含伪市价的次数上限和“未到位”处理）。
	var restingPrice shared.Decimal
	if candidate.resting {
		var restingOK bool
		if restingPrice, restingOK = e.restingPrice(ctx, candidate); !restingOK {
			candidate.resting = false
		}
	}
	pseudoMarket := env.settings.OrderMode == ordersettings.OrderModePseudoMarket && !candidate.resting
	if pseudoMarket {
		if attempt = pseudoMarketAttempts(children); attempt >= env.settings.MaxAttempts {
			reason := fmt.Sprintf("伪市价单已尝试 %d 次仍没成交完，本份目标不再重试", attempt)
			e.warnOnce("unfilled|"+target.TargetID+"|"+candidate.instrument.InstrumentID,
				fmt.Sprintf("组合 %s 的 %s 在账户 %s 上%s", target.LogicalAccountID, candidate.instrument.InstrumentID, accountID, reason))
			tally.capacityErrors = append(tally.capacityErrors, accountID+": "+reason)
			tally.unfilled = true
			return placement{}, false, nil
		}
	}
	accountChildren := childrenOfAccount(children, accountID)
	sliceLimit := env.sliceNotionalLimit(target, candidate.instrument.InstrumentID, len(accountChildren))
	_, minimum, err := baseQuantityRules(candidate.instrument)
	if err != nil {
		return placement{}, true, err
	}
	maxQuantity := floorToStep(sliceLimit.Div(quote.Price), mustBaseStep(candidate.instrument))
	// 整手市场（A 股）单笔上限不足一手时按一手下单（拆单随机化后也一样）：高价股一手就超过默认的 10 万元上限，
	// 否则永远下不了单。
	if exchange.RuleFor(candidate.instrument.MarketType).BoardLot && maxQuantity.Cmp(minimum) < 0 {
		maxQuantity = minimum
	}
	if quantity.Cmp(maxQuantity) > 0 {
		quantity, oddFull = maxQuantity, false
	}
	if quantity.Cmp(shared.Zero()) <= 0 || (quantity.Cmp(minimum) < 0 && !oddFull) {
		tally.capacityErrors = append(tally.capacityErrors, candidate.member.account.TradingAccountID+": 单笔子单上限低于交易所最小数量")
		return placement{}, false, nil
	}
	reversal := reversalCount(accountChildren, candidate)
	if reversal >= 2 {
		// 同一目标内第二次反向：整条标的本目标内不再下单，新目标到达时自然清零。
		if e.Observe != nil && !dryRun {
			e.Observe.Blocked(candidate.member.account.TradingAccountID, "reversal_blocked")
		}
		return placement{reason: reasonTargetReversal}, true, nil
	}
	if dryRun {
		return placement{viable: true}, true, nil
	}
	runnerID := target.RunnerID
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: candidate.member.account.TradingAccountID,
			ClientOrderID:    childClientOrderID(attempt),
			InstrumentID:     candidate.instrument.InstrumentID,
			Type:             exchange.OrderTypeMarket,
			Side:             sideForDelta(candidate.delta),
			Quantity:         quantity,
		},
		Attempt:        attempt,
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReducePositionOnly: candidate.reducing,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerTarget, OwnerID: target.TargetID,
			LogicalAccountID: target.LogicalAccountID, RunnerID: &runnerID,
		},
		PositionBefore: candidate.member.positions[candidate.instrument.InstrumentID].String(),
	}
	spec.PositionSide = exchange.RuleFor(candidate.instrument.MarketType).PositionSide
	if candidate.resting {
		// 挂单：限价 + GTC，买单挂买一价，卖单挂卖一价。
		spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyGTC, &restingPrice
	}
	if pseudoMarket {
		// 限价 + IOC：价格 = 参考价 ×（1 ± 滑点），部分成交的剩余量下一轮按新的滑点再来。
		// 默认最多 6% 的滑点比币安合约的价格过滤器（约 ±5%）宽，按标的的价格偏离限制夹住，避免最后几次被交易所拒单。
		limit := pseudoMarketPrice(quote.Price, spec.Side, env.settings, attempt, candidate.instrument.PriceTick)
		limit = ClampToPriceBand(limit, CageBasis(quote, spec.Side), spec.Side, candidate.instrument)
		// 涨跌停是绝对价：截在涨停价、跌停价之间（A 股；没有涨跌停的市场原样返回）。
		limit = ClampToPriceLimits(limit, quote)
		spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyIOC, &limit
	}
	if candidate.resting {
		restingLimit := ClampToPriceLimits(*spec.LimitPrice, quote)
		spec.LimitPrice = &restingLimit
	}
	if !candidate.reducing {
		capacity, err := e.Orders.Capacity(ctx, spaceID, spec)
		if err != nil {
			if capacityError(err) {
				tally.capacityErrors = append(tally.capacityErrors, candidate.member.account.TradingAccountID+": "+err.Error())
				return placement{}, false, nil
			}
			return placement{}, true, err
		}
		if capacity.Cmp(shared.Zero()) <= 0 {
			tally.capacityErrors = append(tally.capacityErrors, candidate.member.account.TradingAccountID+": 没有可用资金")
			return placement{}, false, nil
		}
		if capacity.Cmp(spec.Quantity) < 0 {
			spec.Quantity = capacity
		}
	}
	placed, err := e.Orders.Place(ctx, spaceID, spec)
	if err != nil {
		if errors.Is(err, orderapp.ErrInstrumentHalted) {
			// 下单校验看到停牌（报价在执行器取价之后变了）：顺延，不拖住整个目标。
			return placement{deferReason: reasonHalted}, true, nil
		}
		if orderapp.IsPermanentValidationError(err) {
			tally.capacityErrors = append(
				tally.capacityErrors,
				candidate.member.account.TradingAccountID+": "+err.Error(),
			)
			return placement{}, false, nil
		}
		return placement{}, true, err
	}
	if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: spaceID, LogicalAccountID: target.LogicalAccountID}, target); err != nil {
		return placement{}, true, e.discardPendingOnTargetInvalidation(ctx, spaceID, string(placed.ID), err)
	}
	quota[accountID]--
	env.noteSubmitted(candidate.instrument.InstrumentID, spec.Quantity.Mul(quote.Price))
	if reversal == 1 && e.OnReversal != nil {
		e.OnReversal(spaceID, target.LogicalAccountID, candidate.instrument.InstrumentID, e.now())
	}
	if e.Observe != nil {
		e.Observe.ChildPlaced(target.LogicalAccountID, target.TargetID, e.crossTargetReversal(ctx, spaceID, target, candidate))
	}
	// 只落库，批量提交由收敛统一发出；拒单后下一轮换成员，全部被拒时写入 blocked_targets。
	return placement{placed: true, orderID: string(placed.ID)}, true, nil
}

// placementCandidates 列出可以承接这次动作的成员：加仓是全部支持该标的的成员，
// 减仓是全部同向持仓的成员，首选成员在最前。减仓数量不超过各成员自己的持仓。
func placementCandidates(action laneAction, members []memberState) []laneAction {
	instrumentID := action.instrument.InstrumentID
	if !action.reducing {
		candidates := make([]laneAction, 0, len(members))
		for _, member := range members {
			if instrument, ok := member.instruments[instrumentID]; ok && !member.restricted(instrumentID) {
				candidates = append(candidates, laneAction{member: member, instrument: instrument, delta: action.delta, resting: action.resting})
			}
		}
		return candidates
	}
	candidates := []laneAction{action}
	for _, member := range positionsByAbsoluteSize(members, instrumentID) {
		position := member.positions[instrumentID]
		instrument, ok := member.instruments[instrumentID]
		if !ok || member.account.TradingAccountID == action.member.account.TradingAccountID ||
			position.IsZero() || position.IsNegative() == action.delta.IsNegative() {
			continue
		}
		delta := action.delta
		if position.Abs().Cmp(delta.Abs()) < 0 {
			delta = position.Neg()
		}
		candidates = append(candidates, laneAction{member: member, instrument: instrument, delta: delta, reducing: true, resting: action.resting})
	}
	return candidates
}

func (e *Executor) checkTargetExecutable(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
) error {
	if target.InstanceID == "" || target.SessionID == "" || target.StrategyID == "" ||
		target.BarEndTime <= 0 || target.EffectiveAt != target.BarEndTime || target.ValidUntil <= target.EffectiveAt {
		return ErrInvalidTarget
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	if !now.Before(time.UnixMilli(target.ValidUntil).UTC()) {
		return ErrTargetExpired
	}
	if now.Before(time.UnixMilli(target.EffectiveAt).UTC()) {
		return errTargetNotEffective
	}
	// Always re-read the authorization immediately before an order can be
	// submitted. The initial Converge snapshot protects the read path; this
	// second read closes the authorization-change window during quote/prepare.
	fresh, err := e.Store.GetLogicalAccount(ctx, logicalAccount.SpaceID, logicalAccount.LogicalAccountID)
	if err != nil {
		return err
	}
	logicalAccount = fresh
	if logicalAccount.ControlMode != "STRATEGY" {
		return ErrTargetSession
	}
	if logicalAccount.OwnerInstanceID != target.InstanceID || logicalAccount.OwnerSessionID != target.SessionID {
		return ErrTargetSession
	}
	return nil
}

func belowMinimumNotional(
	quantity shared.Decimal,
	price shared.Decimal,
	rawMinimum string,
) (bool, error) {
	if strings.TrimSpace(rawMinimum) == "" {
		return false, nil
	}
	minimum, err := shared.ParseDecimal(rawMinimum)
	if err != nil || minimum.IsNegative() {
		return false, fmt.Errorf("%w: minimum notional", ErrInvalidTarget)
	}
	return minimum.Cmp(shared.Zero()) > 0 &&
		quantity.Mul(price).Cmp(minimum) < 0, nil
}

// childQuantity 返回动作按步长取整后的下单数量；不到交易所最小数量时返回原因。
func childQuantity(action laneAction) (shared.Decimal, string, error) {
	step, minimum, err := baseQuantityRules(action.instrument)
	if err != nil {
		return shared.Decimal{}, "", err
	}
	// 整手市场卖出全部可卖数量时允许零股，不按步长取整。
	if oddLotFullSell(action) {
		return action.member.sellable[action.instrument.InstrumentID], "", nil
	}
	quantity := floorToStep(action.delta.Abs(), step)
	if quantity.IsZero() || quantity.Cmp(minimum) < 0 {
		return shared.Zero(), "数量低于交易所最小值", nil
	}
	return quantity, "", nil
}

func (e *Executor) stopOrder(
	ctx context.Context,
	target store.LogicalAccountTargetRecord,
	current store.OrderRecord,
) error {
	if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: current.SpaceID, LogicalAccountID: target.LogicalAccountID}, target); err != nil {
		return err
	}
	switch orderdomain.State(current.State) {
	case orderdomain.Pending:
		_, err := e.Orders.DiscardPending(ctx, current.SpaceID, current.OrderID)
		return err
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		_, err := e.Orders.ResolveUnknown(ctx, current.SpaceID, current.OrderID)
		return err
	default:
		if restingOrder(current) {
			// 上一份目标留下的挂单在阶段 1 撤掉，不带进新目标的差额。
			_, err := e.Orders.Cancel(ctx, current.SpaceID, current.OrderID)
			return err
		}
		// 挂单中、部分成交、撤单中的目标市价单都等它自己结束：撤单只会和成交赛跑，
		// 结束后再按最新持仓重算，避免买了又撤、撤了又买。
		return nil
	}
}

func (e *Executor) pauseLogicalAccount(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target store.LogicalAccountTargetRecord,
	reason string,
) error {
	if err := e.checkTargetExecutable(ctx, logicalAccount, target); err != nil {
		return err
	}
	// 只在组合仍在自动执行时暂停：收敛中途被别处（外部订单、逐仓持仓、风控）暂停的，保留它们写的原因。
	if err := e.Store.Transaction(ctx, func(tx *store.Tx) error {
		_, err := tx.PauseLogicalAccountIf(
			logicalAccount.SpaceID,
			logicalAccount.LogicalAccountID,
			"ACTIVE",
			"",
			store.AutoPauseReason(reason),
		)
		return err
	}); err != nil {
		return err
	}
	target.LastError = reason
	return e.updateTarget(ctx, &target, StatusPending)
}

func (e *Executor) updateTarget(
	ctx context.Context,
	target *store.LogicalAccountTargetRecord,
	status string,
) error {
	// Finishing a completed action must not fail merely because its external
	// call consumed the candidate budget. No new order is authorized here.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := e.checkTargetExecutable(ctx, store.LogicalAccountRecord{SpaceID: target.SpaceID, LogicalAccountID: target.LogicalAccountID}, *target); err != nil {
		return err
	}
	target.Status = status
	updated, err := e.Store.UpdateLogicalAccountTargetState(ctx, *target)
	if err != nil {
		return err
	}
	if !updated {
		return store.ErrConflict
	}
	return nil
}

func capacityError(err error) bool {
	return errors.Is(err, orderapp.ErrInsufficientFunds) ||
		errors.Is(err, orderapp.ErrNotionalLimit) ||
		errors.Is(err, orderapp.ErrLeverageLimit)
}

func targetSubmitConflict(err error) bool {
	return errors.Is(err, orderapp.ErrExternalConflict) ||
		errors.Is(err, orderapp.ErrAutomationPaused) ||
		errors.Is(err, orderapp.ErrTargetOwnerConflict) ||
		errors.Is(err, orderapp.ErrAccountNotReady) ||
		errors.Is(err, orderapp.ErrTargetExpired)
}

func (e *Executor) discardPendingOnTargetInvalidation(
	ctx context.Context,
	spaceID string,
	orderID string,
	err error,
) error {
	if !errors.Is(err, ErrTargetExpired) && !errors.Is(err, orderapp.ErrTargetExpired) &&
		!errors.Is(err, ErrTargetSession) && !errors.Is(err, ErrInvalidTarget) {
		return err
	}
	_, discardErr := e.Orders.DiscardPending(ctx, spaceID, orderID)
	if discardErr != nil {
		return errors.Join(err, discardErr)
	}
	return err
}

// releaseExpiredTargetOrders 在目标过期时释放组合里还没结束的目标单：待提交的丢弃，结果未知的先解析，簿上的挂单撤掉。
// 不只看过期的这一份目标：新目标在第 1 阶段之前就过期（例如成员一直未就绪）时，上一份目标留下的挂单也要撤掉，
// 否则它们一直挂在簿上，等下一份目标到来才被处理。限价 GTC 单没解析出来、没撤下来时返回错误，由调用方按 expiryRetryDelay 重试。
func (e *Executor) releaseExpiredTargetOrders(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
) error {
	if target.LogicalAccountID == "" || target.TargetID == "" {
		return nil
	}
	return e.releaseTargetOrders(ctx, spaceID, target.LogicalAccountID, "")
}

// ReleaseTargetOrders 在策略会话释放、重绑、认领（删掉目标）时撤掉组合里所有还没结束的目标单，调用方持有组合锁：
// 待提交的丢弃；结果未知的（限价 GTC、市价、IOC 都算）先解析一次，退回待提交的丢弃；簿上的挂单撤掉；撤单中的等它自己结束。
// 撤完之后每个撤过单的账户同步一次确认撤单结果（不是每笔都同步）。
// 限价 GTC 单没撤下来（撤单失败、结果解析不出来）时返回错误，调用方稍后重试，不能把挂单留在簿上没人管；
// 市价、IOC 单解析不出来不挡释放，它们提交出去也会自己结束。
func (e *Executor) ReleaseTargetOrders(ctx context.Context, spaceID, logicalAccountID string) error {
	if e == nil || e.Store == nil || e.Orders == nil {
		return ErrExecutorConfig
	}
	return e.releaseTargetOrders(ctx, spaceID, logicalAccountID, "")
}

// errTargetOrderUnresolved 表示目标挂单的提交结果还不知道，撤不了，稍后再试。
var errTargetOrderUnresolved = errors.New("挂单的提交结果还不知道，稍后再撤")

// releaseTargetOrders 释放组合里还没结束的目标单；keepTargetID 非空时跳过这份目标自己的单（第 1 阶段只处理旧目标留下的）。
func (e *Executor) releaseTargetOrders(ctx context.Context, spaceID, logicalAccountID, keepTargetID string) error {
	var open []store.OrderRecord
	const pageSize = 1000
	for offset := 0; ; offset += pageSize {
		orders, total, err := e.Store.ListOrders(ctx, spaceID, store.OrderQuery{
			LogicalAccountID: logicalAccountID,
			OwnerType:        string(orderdomain.OwnerTarget),
			OnlyOpen:         true,
			Offset:           offset,
			Limit:            pageSize,
		})
		if err != nil {
			return err
		}
		for _, current := range orders {
			if keepTargetID == "" || current.OwnerID != keepTargetID {
				open = append(open, current)
			}
		}
		if int64(offset+len(orders)) >= total || len(orders) == 0 {
			break
		}
	}
	var releaseErrs []error
	// resolved 非空表示结果未知的单已经批量解析过（键：单号，值：这笔解析的错误），逐笔处理时不再单独查单。
	var resolved map[string]error
	if keepTargetID != "" {
		// 新目标的第 1 阶段每轮收敛都会来（每秒一次）：结果未知的旧单走受次数限制的批量解析（未知窗口内同一笔最多查两次），
		// 全部解析完每个账户只做一次轻量同步。不能每轮对每笔旧单都查单、拉成交，下单超时故障后会打满交易所限频。
		var unknown []string
		for _, current := range open {
			if state := orderdomain.State(current.State); state == orderdomain.Submitting || state == orderdomain.SubmitUnknown {
				unknown = append(unknown, current.OrderID)
			}
		}
		resolved = make(map[string]error, len(unknown))
		if len(unknown) > 0 {
			results, err := e.Orders.ResolveUnknownBatch(ctx, spaceID, unknown)
			if err != nil {
				releaseErrs = append(releaseErrs, fmt.Errorf("解析结果未知的旧单：%w", err))
			}
			for _, result := range results {
				resolved[result.OrderID] = result.Err
			}
		}
	}
	// canceled 是撤过单、要在最后同步一次的账户。
	canceled := make(map[string]struct{})
	for _, current := range open {
		if err := e.releaseTargetOrder(ctx, current, canceled, resolved); err != nil {
			releaseErrs = append(releaseErrs, fmt.Errorf("目标单 %s：%w", current.OrderID, err))
		}
	}
	if syncer, ok := e.Orders.(deferredCanceler); ok {
		accounts := make([]string, 0, len(canceled))
		for accountID := range canceled {
			accounts = append(accounts, accountID)
		}
		sort.Strings(accounts)
		for _, accountID := range accounts {
			if err := syncer.SyncAfterCancel(ctx, accountID); err != nil {
				releaseErrs = append(releaseErrs, fmt.Errorf("撤单后同步：%w", err))
			}
		}
	}
	return errors.Join(releaseErrs...)
}

// deferredCanceler 是能撤单而不立刻同步、撤完一批再同步一次的订单服务（orderapp.Service 实现）。
type deferredCanceler interface {
	CancelWithoutSync(context.Context, string, string) (orderdomain.Order, error)
	SyncAfterCancel(context.Context, string) error
}

// cancelForRelease 撤一笔目标单：订单服务支持时不在每笔之后同步，把账户记进 canceled，由调用方最后同步一次。
func (e *Executor) cancelForRelease(ctx context.Context, current store.OrderRecord, canceled map[string]struct{}) error {
	deferred, ok := e.Orders.(deferredCanceler)
	if !ok {
		_, err := e.Orders.Cancel(ctx, current.SpaceID, current.OrderID)
		return err
	}
	_, err := deferred.CancelWithoutSync(ctx, current.SpaceID, current.OrderID)
	if err == nil {
		canceled[current.TradingAccountID] = struct{}{}
	}
	return err
}

// releaseTargetOrder 释放一笔目标单。resolved 为 nil 时结果未知的单在这里解析；非 nil 时已经批量解析过，只取那笔的错误。
func (e *Executor) releaseTargetOrder(ctx context.Context, current store.OrderRecord, canceled map[string]struct{}, resolved map[string]error) error {
	state := orderdomain.State(current.State)
	resting := current.OrderType == string(exchange.OrderTypeLimit) && current.TimeInForce == string(exchange.TimeInForceGTC)
	if state == orderdomain.Submitting || state == orderdomain.SubmitUnknown {
		// 每笔结果未知的单都解析一次：查无此单退回待提交的要丢弃，否则目标删掉之后没人再管它。
		// 只有限价 GTC 单可能一直挂在簿上，解析不出来就报错让调用方重试；市价、IOC 单提交出去也会自己结束，解析不出来不挡释放。
		var err error
		if resolved == nil {
			_, err = e.Orders.ResolveUnknown(ctx, current.SpaceID, current.OrderID)
		} else {
			err = resolved[current.OrderID]
		}
		if err != nil {
			if resting {
				return err
			}
			return nil
		}
		resolved, err := e.Store.GetOrder(ctx, current.SpaceID, current.OrderID)
		if err != nil {
			return err
		}
		current, state = resolved, orderdomain.State(resolved.State)
		if state == orderdomain.Submitting || state == orderdomain.SubmitUnknown {
			if resting {
				return errTargetOrderUnresolved
			}
			return nil
		}
	}
	switch {
	case state == orderdomain.Pending:
		_, err := e.Orders.DiscardPending(ctx, current.SpaceID, current.OrderID)
		return err
	case restingOrder(current):
		return e.cancelForRelease(ctx, current, canceled)
	case state == orderdomain.CancelUnknown && resting:
		// 上一次撤单结果未知：撤单可能没到交易所，同步之后会改回挂单中，而那时已经没有目标管它。先确认撤单结果，
		// 确认不了就报错，让调用方稍后重试；撤单中（交易所已受理）的会自己结束，不用管。
		return e.recoverTargetCancel(ctx, current, canceled)
	}
	return nil
}

// cancelRecoverer 是能确认撤单结果的订单服务（orderapp.Service 实现）。
type cancelRecoverer interface {
	RecoverCancel(context.Context, string, string) (orderdomain.Order, error)
}

func (e *Executor) recoverTargetCancel(ctx context.Context, current store.OrderRecord, canceled map[string]struct{}) error {
	recoverer, ok := e.Orders.(cancelRecoverer)
	if !ok {
		return errTargetOrderUnresolved
	}
	if _, err := recoverer.RecoverCancel(ctx, current.SpaceID, current.OrderID); err != nil {
		return err
	}
	latest, err := e.Store.GetOrder(ctx, current.SpaceID, current.OrderID)
	if err != nil {
		return err
	}
	switch state := orderdomain.State(latest.State); {
	case state.Terminal(), state == orderdomain.Canceling:
		return nil
	case restingOrder(latest):
		// 确认下来撤单没生效、订单还挂着：再撤一次。
		return e.cancelForRelease(ctx, latest, canceled)
	}
	return errTargetOrderUnresolved
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{},
			fmt.Errorf("%w: quantity step", ErrInvalidTarget)
	}
	minimum, err := shared.ParseDecimal(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{},
			fmt.Errorf("%w: minimum quantity", ErrInvalidTarget)
	}
	if exchange.RuleFor(instrument.MarketType).ContractValue {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{},
				fmt.Errorf("%w: contract value", ErrInvalidTarget)
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}

func mustBaseStep(instrument store.InstrumentRecord) shared.Decimal {
	step, _, err := baseQuantityRules(instrument)
	if err != nil {
		return shared.Zero()
	}
	return step
}

func floorToStep(value, step shared.Decimal) shared.Decimal {
	return shared.FloorToStep(value, step)
}

func sameSign(left, right shared.Decimal) bool {
	return !left.IsZero() && !right.IsZero() &&
		left.IsNegative() == right.IsNegative()
}

func sideForDelta(delta shared.Decimal) exchange.Side {
	if delta.IsNegative() {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// positionUnreflected 判断持仓是否回退到某笔已成交目标子单下单前的值。对每个成员取最近 10 分钟内
// 不分来源、有成交的订单，最近一笔是目标子单时，从它开始收集同向的目标子单，遇到非目标单或反向单就停；
// 当前持仓等于其中任一笔的下单前持仓就说明持仓没反映这些成交。
func (e *Executor) positionUnreflected(
	ctx context.Context,
	spaceID string,
	members []memberState,
	instrumentID string,
) (bool, error) {
	since := e.now().Add(-positionReflectWindow).UnixMilli()
	for _, member := range members {
		instrument, ok := member.instruments[instrumentID]
		if !ok {
			continue
		}
		records, err := e.Store.RecentFilledOrdersForLane(ctx, spaceID, member.account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol), since)
		if err != nil {
			return false, err
		}
		if len(records) == 0 || records[0].OwnerType != string(orderdomain.OwnerTarget) {
			continue
		}
		current := member.positions[instrumentID]
		side := records[0].Side
		for _, record := range records {
			if record.OwnerType != string(orderdomain.OwnerTarget) || record.Side != side {
				break
			}
			if record.PositionBefore == "" {
				continue
			}
			before, err := shared.ParseDecimal(record.PositionBefore)
			if err != nil {
				return false, err
			}
			if before.Cmp(current) == 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

// childrenOfAccount 取出某个执行账户的子单。
func childrenOfAccount(children []store.TargetChildOrder, tradingAccountID string) []store.TargetChildOrder {
	var mine []store.TargetChildOrder
	for _, child := range children {
		if child.TradingAccountID == tradingAccountID {
			mine = append(mine, child)
		}
	}
	return mine
}

// reversalCount 返回已发出子单（含这次将发的）的方向切换次数。
func reversalCount(children []store.TargetChildOrder, candidate laneAction) int {
	sides := make([]string, 0, len(children)+1)
	for _, child := range children {
		sides = append(sides, child.Side)
	}
	sides = append(sides, string(sideForDelta(candidate.delta)))
	switches := 0
	for index := 1; index < len(sides); index++ {
		if sides[index] != sides[index-1] {
			switches++
		}
	}
	return switches
}

// crossTargetReversal 判断这笔子单是否和 5 分钟内上一个目标在同一标的上的成交方向相反，只用于计数。
func (e *Executor) crossTargetReversal(
	ctx context.Context,
	spaceID string,
	target store.LogicalAccountTargetRecord,
	candidate laneAction,
) bool {
	since := e.now().Add(-5 * time.Minute).UnixMilli()
	records, err := e.Store.RecentFilledOrdersForLane(ctx, spaceID, candidate.member.account.TradingAccountID,
		exchange.NewSymbolKey(candidate.instrument.MarketType, candidate.instrument.ExchangeSymbol), since)
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.OwnerType != string(orderdomain.OwnerTarget) || record.OwnerID == target.TargetID {
			continue
		}
		return record.Side != string(sideForDelta(candidate.delta))
	}
	return false
}

// batchMarket 是决定批次额度的市场：开通了合约的账户按合约算（额度更大），否则取第一个市场。
func batchMarket(account store.TradingAccountRecord) exchange.MarketType {
	for _, market := range account.Markets {
		if exchange.RuleFor(market).UsesMargin {
			return exchange.MarketType(market)
		}
	}
	if len(account.Markets) == 0 {
		return exchange.MarketTypeUnspecified
	}
	return exchange.MarketType(account.Markets[0])
}

// batchQuota 是每个执行账户本批可下的单数。
func (e *Executor) batchQuota(members []memberState) map[string]int {
	quota := make(map[string]int, len(members))
	for _, member := range members {
		size := 0
		if e.BatchSize != nil {
			size = e.BatchSize(string(batchMarket(member.account)))
		}
		if size <= 0 {
			size = defaultSwapBatchSize
			if !exchange.RuleFor(batchMarket(member.account)).UsesMargin {
				size = defaultSpotBatchSize
			}
		}
		quota[member.account.TradingAccountID] = size
	}
	return quota
}

// batchAllowed 判断现在能否发新批：批间隔已到，成员接口没有限流暂停，权重用量低于 90%。
// 只因批间隔未到而不能发时，retryAfter 是还要等的时间；限流原因由定时器重试。
func (e *Executor) batchAllowed(logicalAccountID string, members []memberState, settings ordersettings.Settings) (bool, time.Duration) {
	now := e.now()
	if interval := e.batchInterval(members, settings); interval > 0 {
		e.batchMu.Lock()
		last := e.lastBatch[logicalAccountID]
		e.batchMu.Unlock()
		if elapsed := now.Sub(last); !last.IsZero() && elapsed < interval {
			return false, interval - elapsed
		}
	}
	if e.RateStatus == nil {
		return true, 0
	}
	for _, member := range members {
		status, ok := e.RateStatus(member.account.TradingAccountID)
		if !ok {
			continue
		}
		if status.Paused(now) || now.Before(status.OrdersPausedUntil) || status.UsageRatio() >= batchUsageLimit {
			return false, 0
		}
	}
	return true, 0
}

// batchInterval 返回这个组合账户的批间隔：下单设置里写了就用设置的（实盘、模拟盘都一样），
// 没写时取执行器的默认值，成员全部是模拟盘时用模拟盘的默认值。
func (e *Executor) batchInterval(members []memberState, settings ordersettings.Settings) time.Duration {
	if settings.BatchIntervalSet {
		return settings.BatchInterval
	}
	if e.PaperMinBatchInterval <= 0 || len(members) == 0 {
		return e.MinBatchInterval
	}
	for _, member := range members {
		if exchange.ExecutionMode(member.account.ExecutionMode) != exchange.ExecutionModePaper {
			return e.MinBatchInterval
		}
	}
	return e.PaperMinBatchInterval
}

func (e *Executor) noteBatch(logicalAccountID string) {
	e.batchMu.Lock()
	defer e.batchMu.Unlock()
	if e.lastBatch == nil {
		e.lastBatch = make(map[string]time.Time)
	}
	e.lastBatch[logicalAccountID] = e.now()
}

// handlePendingChild 处理当前目标的待提交子单。从未发出的是本地残留，丢弃后按最新事实重算；
// 未知窗口过后查无此单回到待提交的单，差额变了就丢弃重算，否则用同一单号串行重发，遇到未就绪就保留并停止本轮。
// handled 为真时 Converge 直接返回 result 和 err。
func (e *Executor) handlePendingChild(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	current store.OrderRecord,
	desired map[string]desiredTarget,
	members []memberState,
	env *laneEnv,
) (Result, bool, error) {
	spaceID := current.SpaceID
	if current.SubmittedAt == 0 {
		if _, err := e.Orders.DiscardPending(ctx, spaceID, current.OrderID); err != nil {
			return Result{}, false, err
		}
		return Result{Action: "discard"}, false, nil
	}
	if err := e.checkTargetExecutable(ctx, logicalAccount, *target); err != nil {
		if expiryErr := e.discardPendingOnTargetInvalidation(ctx, spaceID, current.OrderID, err); expiryErr != nil {
			return Result{}, true, expiryErr
		}
		return Result{Status: StatusPaused}, true, err
	}
	action, complete, _, err := e.laneAction(env, current.InstrumentID, desired[current.InstrumentID], members)
	if err != nil {
		return Result{}, false, err
	}
	quantity, parseErr := shared.ParseDecimal(current.Quantity)
	if parseErr != nil {
		return Result{}, false, parseErr
	}
	unreflected, checkErr := e.positionUnreflected(ctx, spaceID, members, current.InstrumentID)
	if checkErr != nil {
		return Result{}, false, checkErr
	}
	// 差额变了（方向、成员或剩余数量小于这笔单）或持仓没反映已成交子单时丢弃重算，不按旧数量重发。
	if complete || unreflected || string(sideForDelta(action.delta)) != current.Side ||
		action.member.account.TradingAccountID != current.TradingAccountID ||
		action.delta.Abs().Cmp(quantity) < 0 {
		if _, err := e.Orders.DiscardPending(ctx, spaceID, current.OrderID); err != nil {
			return Result{}, false, err
		}
		return Result{Action: "discard"}, false, nil
	}
	submitted, err := e.Orders.Submit(ctx, spaceID, current.OrderID)
	if err == nil || submitted.State == orderdomain.Rejected {
		return Result{Action: "submit"}, false, nil
	}
	switch {
	case errors.Is(err, orderdomain.ErrReferencePriceStale):
		// 报价过期时丢弃这笔子单，下一轮按最新持仓和报价重新下单。
		if _, discardErr := e.Orders.DiscardPending(ctx, spaceID, current.OrderID); discardErr != nil {
			return Result{}, true, errors.Join(err, discardErr)
		}
		return Result{Action: "discard"}, false, nil
	case errors.Is(err, orderapp.ErrAccountNotReady):
		// 保留待提交和原单号，等账户就绪后再发。
		target.LastError = err.Error()
		if updateErr := e.updateTarget(ctx, target, StatusPending); updateErr != nil {
			return Result{}, true, updateErr
		}
		return Result{Status: StatusPaused}, true, nil
	case exchange.NotAccepted(err):
		// 限流：订单回到待提交，下一轮按规则处理。
		return Result{Action: "submit"}, false, nil
	case targetSubmitConflict(err):
		if _, discardErr := e.Orders.DiscardPending(ctx, spaceID, current.OrderID); discardErr != nil {
			return Result{}, true, errors.Join(err, discardErr)
		}
		if errors.Is(err, orderapp.ErrTargetExpired) {
			return Result{Status: StatusPaused}, true, err
		}
		if pauseErr := e.pauseLogicalAccount(ctx, logicalAccount, *target, err.Error()); pauseErr != nil {
			return Result{}, true, pauseErr
		}
		return Result{Status: StatusPaused, Action: "pause"}, true, nil
	}
	return Result{}, true, err
}

// placementFailure 处理下单落库时的目标级错误。
func (e *Executor) placementFailure(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	placeErr error,
) (Result, error) {
	if !targetSubmitConflict(placeErr) {
		return Result{}, placeErr
	}
	if errors.Is(placeErr, orderapp.ErrTargetExpired) {
		return Result{}, placeErr
	}
	if errors.Is(placeErr, orderapp.ErrAccountNotReady) {
		target.LastError = placeErr.Error()
		if err := e.updateTarget(ctx, target, StatusPending); err != nil {
			return Result{}, err
		}
	} else if err := e.pauseLogicalAccount(ctx, logicalAccount, *target, placeErr.Error()); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusPaused, Action: "pause"}, nil
}

// batchFailure 处理批量提交里的目标级错误。拒单、限流只影响单笔，留给下一轮；
// 目标过期、账户未就绪、归属冲突影响整个组合账户（handled 为真，调用方直接返回）。没发出的单已经退回待提交，下一轮丢弃。
// 其它错误不算处理：handled 为假，同时返回第一个错误，调用方照常刷新目标状态后把它带回去。
func (e *Executor) batchFailure(
	ctx context.Context,
	logicalAccount store.LogicalAccountRecord,
	target *store.LogicalAccountTargetRecord,
	results []orderapp.BatchResult,
) (Result, bool, error) {
	var other error
	for _, result := range results {
		err := result.Err
		if err == nil || result.Order.State == orderdomain.Rejected || exchange.NotAccepted(err) {
			continue
		}
		if targetSubmitConflict(err) {
			// 没发出的子单立即释放预留，不留到下一轮。
			if _, discardErr := e.Orders.DiscardPending(ctx, target.SpaceID, result.OrderID); discardErr != nil {
				return Result{}, true, errors.Join(err, discardErr)
			}
		}
		switch {
		case errors.Is(err, orderapp.ErrTargetExpired):
			return Result{Status: StatusPaused}, true, err
		case errors.Is(err, orderapp.ErrAccountNotReady):
			target.LastError = err.Error()
			if updateErr := e.updateTarget(ctx, target, StatusPending); updateErr != nil {
				return Result{}, true, updateErr
			}
			return Result{Status: StatusPaused}, true, nil
		case targetSubmitConflict(err):
			if pauseErr := e.pauseLogicalAccount(ctx, logicalAccount, *target, err.Error()); pauseErr != nil {
				return Result{}, true, pauseErr
			}
			return Result{Status: StatusPaused, Action: "pause"}, true, nil
		}
		if other == nil {
			other = err
		}
	}
	return Result{}, false, other
}

func quotaExhausted(quota map[string]int) bool {
	for _, remaining := range quota {
		if remaining > 0 {
			return false
		}
	}
	return true
}

// waitingOnChildren 报告组合账户是否只是在等当前目标已发出的子单（挂单中、部分成交、撤单中）结束。
// 有待提交、提交中、结果未知的子单，有其他归属的活动订单，子单所在成员未就绪，
// 或成员账户上有不属于当前目标的活动订单（例如入组前的外部单）时返回 false，交给完整流程处理。
func (e *Executor) waitingOnChildren(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	target store.LogicalAccountTargetRecord,
) (bool, error) {
	records, _, err := e.Store.ListOrders(ctx, spaceID, store.OrderQuery{LogicalAccountID: logicalAccountID, OnlyOpen: true, Limit: 1000})
	if err != nil {
		return false, err
	}
	waiting := false
	accounts := make(map[string]struct{})
	for _, record := range records {
		state := orderdomain.State(record.State)
		if state.Terminal() {
			continue
		}
		// 系统单（BNB 补买、稳定币调平）和收敛无关，稳定币买单可能挂一个小时：不能让它把快路径一直挡住。
		if record.OwnerType == string(orderdomain.OwnerSystem) {
			continue
		}
		if record.OwnerType != string(orderdomain.OwnerTarget) || record.OwnerID != target.TargetID {
			return false, nil
		}
		if restingOrder(record) {
			// 挂单不阻塞：交给完整流程判断是否只剩挂单。
			continue
		}
		switch state {
		case orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown:
			return false, nil
		}
		waiting = true
		accounts[record.TradingAccountID] = struct{}{}
	}
	for accountID := range accounts {
		account, err := e.Store.GetTradingAccountByID(ctx, accountID)
		if err != nil {
			return false, err
		}
		if account.Status != "ENABLED" || !account.Ready {
			return false, nil
		}
		active, err := e.Store.ListActiveOrdersForAccount(ctx, spaceID, accountID)
		if err != nil {
			return false, err
		}
		for _, order := range active {
			if order.OwnerType == string(orderdomain.OwnerSystem) {
				continue
			}
			if order.OwnerType != string(orderdomain.OwnerTarget) || order.OwnerID != target.TargetID {
				return false, nil
			}
		}
	}
	return waiting, nil
}

// listedBlocked 返回写进目标 blocked_targets 的列表：真实阻塞在前（监控关注项只取第一条），顺延的在后。
// 同一个标的只列一条（blocked_targets 不允许重复标的）：已经有真实阻塞的标的不再列它的顺延。
func listedBlocked(blocked, deferred []store.BlockedTarget) []store.BlockedTarget {
	if len(deferred) == 0 {
		return blocked
	}
	listed := make(map[string]struct{}, len(blocked)+len(deferred))
	merged := make([]store.BlockedTarget, 0, len(blocked)+len(deferred))
	for _, item := range blocked {
		listed[item.InstrumentID] = struct{}{}
		merged = append(merged, item)
	}
	for _, item := range deferred {
		if _, dup := listed[item.InstrumentID]; dup {
			continue
		}
		listed[item.InstrumentID] = struct{}{}
		merged = append(merged, item)
	}
	return merged
}
