// Package wallet 实现币安普通账户的钱包划转与归集（执行计划 4.5、T9）：
// 现货买入前（第 4 阶段）把资金从合约钱包划进现货钱包，目标执行完后（第 6 阶段）把现货钱包多出来的资金归集回合约钱包。
// 统一账户、OKX 和模拟盘不划转。划转只在同一个账户的两个钱包之间进行，没有提现和跨账户路径。
//
// 币安的万向划转接口没有客户端流水号：先写一条 PENDING 记录再请求；请求超时、进程崩溃时，
// 靠记录的请求时间、方向、资产和金额去对账交易所的划转记录，确认没到账才重试，不会重复划转。
// 阶段不单独存储，每一轮都从划转表和当前余额推出该做什么，重启后接着走。
//
// 单个账户的问题（额度不足、划转被拒绝、查询失败）不会让整轮收敛失败：这里不返回账户级错误，
// 而是把买单比例设为 0 并写明原因（执行器写进 blocked_targets），或者返回“等待”并给出退避时间。
package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// buyMarginRatio 是现货买单名义总额的放大系数：留出手续费和报价波动的余量。
	buyMarginRatio = "1.02"
	// transferCapRatio 是合约钱包可划转额度里实际划出的比例，留一点余地给同时发生的保证金变化。
	transferCapRatio = "0.99"
	// minTransferAmount 是低于它就不划转的金额（USDT）：避免为几分钱来回划转。
	minTransferAmount = "1"
	// collectKeepAmount 是归集后现货钱包里至少留下的 USDT。
	collectKeepAmount = "1"
	// collectMinThreshold、collectEquityRatio：多出来的资金低于 max(100U, 权益 × 1%) 不归集，避免 1 分钟级策略每期来回划转。
	collectMinThreshold = "100"
	collectEquityRatio  = "0.01"
	// confirmGrace 是请求交易所后还没有拿到流水号时，至少等多久才能断定“没有到账”：交易所的划转记录可能有延迟。
	confirmGrace = time.Minute
	// historyLookback 是对账时回看交易所划转记录的提前量（本地与交易所时钟有偏差）。
	historyLookback = 2 * time.Minute
	// accountWindow 是按账户查未结划转记录的时间窗口。
	accountWindow = 24 * time.Hour
	// snapshotGrace 是划转完成后等现货钱包快照追上的最长时间：超过就放行（买入前的余额是现查的，
	// 校验用的存储快照稍旧最多让交易所拒单，不会多花钱）。
	snapshotGrace = 2 * time.Minute
	// maxTransferFailures 是同一个目标、同一方向连续硬失败（被拒绝、余额不足、交易所记录显示失败）多少次后不再重试。
	maxTransferFailures = 3
	// maxTransfersPerTarget 是同一个目标往现货钱包划转的次数上限（没被额度封顶的划转完成后仍缺钱会再划，价格大幅上涨时才会发生）：
	// 防止余额口径异常时一直追加划转。
	maxTransfersPerTarget = 3
	// queryFailureAlert 是同一个账户连续查询失败多少次后告警；重试间隔指数退避（见 accountBackoff），最长 maxBackoff。
	queryFailureAlert = 5
	maxBackoff        = 30 * time.Second
	// pendingAlertAfter 是划转记录一直处于 PENDING 超过多久后告警（同账户不再发起新划转）。
	pendingAlertAfter = 10 * time.Minute
)

// 不计入“连续失败”的 FAILED 记录标记：限频没有发出请求、宽限期后交易所记录里没有这笔划转，都不是划转本身出了问题。
const (
	softNotSent   = "[未发出]"
	softNotArrive = "[未到账]"
)

// 合约钱包可划转的资金不够时，写进 blocked_targets 的原因都以 FundsShortagePrefix 开头：
// 执行器据此判断“这个账户的买单在等资金”（不归集），关注项据此把它合并进 transfer_shortfall（不再重复发 target_blocked）。
const (
	FundsShortagePrefix = "合约钱包可划转的资金"
	// ReasonShortfallNoBuy 是资金不足、钱包没有给出更具体原因时，这轮买不了的说明。
	ReasonShortfallNoBuy = FundsShortagePrefix + "不足，这轮买不了"
	// ReasonShortfallScaled 是资金不足、买单按现有资金缩小的说明。
	ReasonShortfallScaled = FundsShortagePrefix + "不足，现货买入按现有资金缩小"
	reasonCapacityUsedUp  = FundsShortagePrefix + "已经划完，现货买入按现有资金缩小"
	// reasonAlreadyCollected 不是资金不足：这个目标已经把现货钱包多出来的资金归集回合约钱包，不再追加划转。
	reasonAlreadyCollected = "这个目标已经把现货钱包多出来的资金归集回合约钱包，不再追加划转，现货买入按现有资金缩小"
)

// IsFundsShortage 报告 blocked_targets 的原因是不是合约钱包可划转资金不足。
func IsFundsShortage(reason string) bool { return strings.HasPrefix(reason, FundsShortagePrefix) }

// AdapterSource 按执行账户取适配器。
type AdapterSource interface {
	Adapter(accountID string) (execution.ExecutionAdapter, error)
}

// Buy 是一个需要划转资金的现货买入：账户、标的和计划买入的基础资产数量（金额由划转实现按报价折算）。
type Buy struct {
	Account    store.TradingAccountRecord
	Instrument store.InstrumentRecord
	Quantity   shared.Decimal
}

// Outcome 是第 4 阶段一次划转检查的结果。
type Outcome struct {
	// Waiting 是本轮还不能买入的账户（键：执行账户 ID）：正在划转、对账、等现货钱包快照，或查询、划转软失败后在退避等待。
	// 值是给人看的原因，空串表示正常等待、不用显示。只有这些账户的买单被挡，同组合的其他账户照常下单。
	Waiting map[string]string
	// BuyScale 是各账户现货买入要缩小到的比例（键：执行账户 ID，0 到 1）；没有条目表示不缩小。
	BuyScale map[string]shared.Decimal
	// Reasons 是比例缩小或为 0 的原因（键：执行账户 ID），执行器写进 blocked_targets。
	Reasons map[string]string
	// RetryAfter 是建议的下次收敛间隔（失败后退避的剩余时间）；0 表示用默认间隔。
	RetryAfter time.Duration
}

// Service 是普通账户的钱包划转与归集。
type Service struct {
	Store    *store.Store
	Adapters AdapterSource
	// RequestSync 请求会话尽快做一次完整同步（划转完成后等现货钱包的快照追上）；为空时只等周期同步。
	RequestSync func(accountID string)
	Now         func() time.Time
	NewID       func() string

	mu sync.Mutex
	// alerted 记录已经告警过的（目标、账户、原因），同一个目标同一个原因只告警一次。
	alerted map[string]struct{}
	// backoffs 是各账户的重试状态：连续查询失败、划转软失败的次数和下次允许请求交易所的时间。
	backoffs map[string]*accountBackoff
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	id, err := gonanoid.New()
	if err != nil {
		panic(fmt.Sprintf("generate transfer ID: %v", err))
	}
	return id
}

func dec(value string) shared.Decimal { return shared.MustDecimal(value) }

// transferer 取账户适配器的划转能力。
func (s *Service) transferer(accountID string) (execution.ExecutionAdapter, execution.WalletTransferer, error) {
	adapter, err := s.Adapters.Adapter(accountID)
	if err != nil {
		return nil, nil, err
	}
	transferer, ok := adapter.(execution.WalletTransferer)
	if !ok || !execution.Supports(adapter, execution.CapabilityTransfer) {
		return nil, nil, errUnsupported
	}
	return adapter, transferer, nil
}

var errUnsupported = errors.New("适配器不支持钱包划转")

// alertOnce 同一个（目标、账户、原因）只告警一次。
func (s *Service) alertOnce(kind, targetID, accountID, detail string) {
	s.alertOnceWithLabel(kind, targetID, accountID, detail, detail)
}

// alertOnceWithLabel 同上；detail 写日志（可以带数字），label 进关注项（Monitor 的标签值）：
// 必须是固定文字，带着会变的数字每份目标都会产生新的标签值，告警恢复不了。
func (s *Service) alertOnceWithLabel(kind, targetID, accountID, detail, label string) {
	key := kind + "|" + targetID + "|" + accountID
	s.mu.Lock()
	if s.alerted == nil || len(s.alerted) > 10000 {
		s.alerted = map[string]struct{}{}
	}
	_, seen := s.alerted[key]
	s.alerted[key] = struct{}{}
	s.mu.Unlock()
	if seen {
		return
	}
	log.Warnf("交易划转：%s", detail)
	telemetry.Note(kind, label, s.now())
}

// accountBackoff 是一个账户的重试状态：查询失败和划转软失败各自连续计数，next 之前不再请求交易所。
type accountBackoff struct {
	queryFails int
	softFails  int
	// next 是下一次允许请求交易所的时间；reason 是等待期间给人看的原因。
	next   time.Time
	reason string
}

// backoffFor 是第 count 次连续失败后的等待时间：1 秒起指数退避，最长 30 秒。
func backoffFor(count int) time.Duration {
	return min(time.Second<<min(max(count, 1)-1, 5), maxBackoff)
}

// waitRemaining 返回账户还要等多久才能再请求交易所（0 表示可以了）和等待的原因。
// 目标每秒收敛一次，退避必须在这里生效：到点前直接返回等待，不调交易所（也不会每秒发请求、很快刷出告警）。
func (s *Service) waitRemaining(accountID string) (time.Duration, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backoff := s.backoffs[accountID]
	if backoff == nil {
		return 0, ""
	}
	if remaining := backoff.next.Sub(s.now()); remaining > 0 {
		return remaining, backoff.reason
	}
	return 0, ""
}

// scheduleRetry 记一次失败并安排下次重试时间；soft 为真记划转软失败，否则记查询失败。返回等待时间和连续失败次数。
func (s *Service) scheduleRetry(accountID, reason string, soft bool) (time.Duration, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoffs == nil {
		s.backoffs = map[string]*accountBackoff{}
	}
	backoff := s.backoffs[accountID]
	if backoff == nil {
		backoff = &accountBackoff{}
		s.backoffs[accountID] = backoff
	}
	count := &backoff.queryFails
	if soft {
		count = &backoff.softFails
	}
	*count++
	wait := backoffFor(*count)
	backoff.next, backoff.reason = s.now().Add(wait), reason
	return wait, *count
}

// queryFailed 记一次账户的查询失败（余额、报价、划转记录），安排退避重试；连续失败到阈值告警。账户这轮等待，不买。
func (s *Service) queryFailed(account store.TradingAccountRecord, err error) accountResult {
	summary := truncate(exchange.FailureSummary(err), 200)
	reason := "读取账户余额或划转记录失败（" + summary + "），稍后自动重试"
	wait, count := s.scheduleRetry(account.TradingAccountID, reason, false)
	if count >= queryFailureAlert {
		log.Warnf("交易划转：账户 %s 连续 %d 次查询失败：%v", account.Name, count, err)
		// 关注项用固定文字（次数和失败说明会变，只写日志）。
		telemetry.Note(telemetry.AttentionTransferFailing, account.Name+"：划转前的余额或划转记录连续多次查询失败", s.now())
	}
	return accountResult{waiting: true, reason: reason, retryAfter: wait}
}

// queryOK 记账户的查询成功：查询失败的连续计数清零（划转软失败的计数只在划转发出后清零）。
func (s *Service) queryOK(accountID string) {
	s.mu.Lock()
	if backoff := s.backoffs[accountID]; backoff != nil {
		backoff.queryFails = 0
	}
	s.mu.Unlock()
}

// transferSent 记账户的划转请求已经被交易所受理（或结果不明交给对账）：软失败的连续计数清零。
func (s *Service) transferSent(accountID string) {
	s.mu.Lock()
	if backoff := s.backoffs[accountID]; backoff != nil {
		backoff.softFails = 0
	}
	s.mu.Unlock()
}

// accountResult 是一个账户在第 4 阶段这一轮的结果。
type accountResult struct {
	// waiting 为真表示这个账户这轮不能买：正在划转、对账、等现货钱包快照，或失败后在退避等待。
	waiting    bool
	scale      *shared.Decimal
	reason     string
	retryAfter time.Duration
}

// BeforeSpotBuy 实现第 4 阶段。对每个账户：
//  1. 退避期内直接返回等待，不请求交易所；
//  2. 先把这个账户所有未结的划转记录（不论哪个目标）对账完；有未结的就等待，不发起新划转（目标 1 超时未到账时目标 2 到达，
//     不会按还没变化的余额再划一笔）；
//  3. 现查现货、合约两个钱包的余额（不用缓存），划转金额 = max(0, 买单名义总额 × 1.02 − 现货钱包可用 USDT)，
//     不超过合约钱包可划转额度（maxWithdrawAmount）× 0.99；不需要划转就直接放行，不受连续失败次数影响；
//  4. 上一笔划转是被额度封顶的（记录上有标记）并且已经完成，不再追加，只按比例缩小买单（不会把合约钱包留的 1% 也用掉）；
//     没封顶的划转完成后仍缺钱（比如价格上涨）就再划一笔；
//  5. 额度不足、划转连续失败、不支持划转：买单比例设 0 或按比例缩小，原因写明并告警；限频等软失败不算失败，
//     这个账户按退避等待后自动重试；一个账户出问题不影响同组合其他账户（结果按账户区分）。
func (s *Service) BeforeSpotBuy(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, buys []Buy) (Outcome, error) {
	outcome := Outcome{Waiting: map[string]string{}, BuyScale: map[string]shared.Decimal{}, Reasons: map[string]string{}}
	byAccount := map[string][]Buy{}
	var order []string
	for _, buy := range buys {
		id := buy.Account.TradingAccountID
		if _, seen := byAccount[id]; !seen {
			order = append(order, id)
		}
		byAccount[id] = append(byAccount[id], buy)
	}
	for _, accountID := range order {
		result := s.beforeSpotBuyForAccount(ctx, spaceID, targetRecord, byAccount[accountID])
		outcome.RetryAfter = max(outcome.RetryAfter, result.retryAfter)
		if result.waiting {
			outcome.Waiting[accountID] = result.reason
			continue
		}
		if result.scale != nil {
			outcome.BuyScale[accountID] = *result.scale
		}
		if result.reason != "" {
			outcome.Reasons[accountID] = result.reason
		}
	}
	return outcome, nil
}

func (s *Service) beforeSpotBuyForAccount(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, buys []Buy) accountResult {
	account := buys[0].Account
	zero := shared.Zero()
	if remaining, reason := s.waitRemaining(account.TradingAccountID); remaining > 0 {
		return accountResult{waiting: true, reason: reason, retryAfter: remaining}
	}
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		reason := fmt.Sprintf("账户钱包划转不可用：%v", err)
		s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID, account.Name+"："+reason)
		return accountResult{scale: &zero, reason: reason}
	}
	// 1. 这个账户所有未结的划转（不论目标）。
	rows, pendingRows, listErr := s.settleAccountPending(ctx, spaceID, account, transferer)
	if listErr != nil {
		return s.queryFailed(account, listErr)
	}
	if len(pendingRows) > 0 {
		return accountResult{waiting: true}
	}
	// 2. 现查报价和余额。
	requirement, err := s.buyRequirement(ctx, adapter, buys)
	if err != nil {
		return s.queryFailed(account, err)
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return s.queryFailed(account, err)
	}
	s.queryOK(account.TradingAccountID)
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	// 3. 刚完成的划转：等现货钱包的快照追上，校验和下单才看得到这笔资金。
	if s.waitingForSnapshot(ctx, spaceID, account, rows) {
		return accountResult{waiting: true}
	}
	need := requirement.Sub(spotAvailable)
	if need.Cmp(dec(minTransferAmount)) < 0 {
		// 缺口不到起划额：现货钱包已经够付买单的名义金额（不含 2% 的手续费、报价余量）就不缩小；
		// 够不上名义金额，只缺一点点，按起划额划转（不超过可划转额度），不能为了不到 1U 的缺口把买单缩得下不了单。
		notional := requirement.Div(dec(buyMarginRatio))
		if need.Cmp(shared.Zero()) <= 0 || spotAvailable.Cmp(notional) >= 0 {
			return accountResult{}
		}
		need = dec(minTransferAmount)
	}
	// 4. 需要划转。这个目标已经把现货钱包多出来的资金归集回合约钱包过：不再往现货钱包划
	// （买单因为非资金原因下不了单时，归集了又划进来会让钱停在现货钱包），只按现有资金缩小。
	collected, err := s.Store.ListWalletTransfersForTarget(ctx, spaceID, targetRecord.LogicalAccountID, targetRecord.TargetID, account.TradingAccountID, store.TransferToFutures)
	if err != nil {
		return s.queryFailed(account, err)
	}
	for _, row := range collected {
		if row.State != store.TransferStateFailed {
			return accountResult{scale: scaleFor(spotAvailable, requirement), reason: reasonAlreadyCollected}
		}
	}
	history, err := s.Store.ListWalletTransfersForTarget(ctx, spaceID, targetRecord.LogicalAccountID, targetRecord.TargetID, account.TradingAccountID, store.TransferToSpot)
	if err != nil {
		return s.queryFailed(account, err)
	}
	completed, capped := completedTransfers(history)
	if capped || completed >= maxTransfersPerTarget {
		// 上一笔已经把合约钱包可划的额度划完（或这个目标划得太多次了）：不再追加，只按现有资金缩小。
		reason := reasonCapacityUsedUp
		if !capped {
			reason = fmt.Sprintf(FundsShortagePrefix+"一直不够（这个目标已经划转 %d 次），现货买入按现有资金缩小", completed)
		}
		s.alertShortfallOnce(account, targetRecord.TargetID, requirement, spotAvailable)
		return accountResult{scale: scaleFor(spotAvailable, requirement), reason: reason}
	}
	failures, lastError := trailingHardFailures(history)
	if failures >= maxTransferFailures {
		reason := fmt.Sprintf("合约钱包划转连续失败 %d 次（%s），这个目标不再重试，现货买入这轮不做", failures, lastError)
		s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID, account.Name+"："+reason)
		return accountResult{scale: &zero, reason: reason}
	}
	capacity := walletTransferable(snapshot, exchange.WalletFutures, asset).Mul(dec(transferCapRatio)).RoundDown(8)
	amount := need
	if amount.Cmp(capacity) > 0 {
		amount = capacity
	}
	if amount.Cmp(dec(minTransferAmount)) < 0 {
		s.alertShortfallOnce(account, targetRecord.TargetID, requirement, spotAvailable)
		return accountResult{scale: scaleFor(spotAvailable, requirement), reason: ReasonShortfallScaled}
	}
	// 被额度封顶的划转在记录上打标记：它完成后这个目标不再追加划转。
	isCapped := amount.Cmp(need) < 0
	if isCapped {
		s.alertShortfallOnce(account, targetRecord.TargetID, requirement, spotAvailable.Add(amount))
	}
	start := s.startTransfer(ctx, spaceID, targetRecord, targetRecord.TargetID, account, store.TransferToSpot, asset, amount, isCapped, len(history), transferer, exchange.WalletFutures, exchange.WalletSpot)
	switch {
	case start.started:
		// 划转已发出（或结果不明、等对账），这一轮不买。
		s.transferSent(account.TradingAccountID)
		return accountResult{waiting: true}
	case start.soft:
		// 限频、本地限流暂停等：请求没有被受理，不是划转本身的问题。安排退避重试，目标保持收敛中，不算失败。
		reason := fmt.Sprintf("合约钱包划转暂时没发出（%s），稍后自动重试", start.failure)
		wait, _ := s.scheduleRetry(account.TradingAccountID, reason, true)
		return accountResult{waiting: true, reason: reason, retryAfter: wait}
	default:
		// 确定没有划成：这一轮这个账户不买，下一轮再试，连续硬失败到上限后不再重试。
		return accountResult{scale: &zero, reason: "合约钱包划转没有成功：" + start.failure}
	}
}

// completedTransfers 返回这个目标已经完成的“划进现货钱包”的划转笔数，以及其中有没有被额度封顶的。
func completedTransfers(history []store.WalletTransferRecord) (count int, capped bool) {
	for _, row := range history {
		if row.State == store.TransferStateCompleted {
			count++
			capped = capped || row.Capped
		}
	}
	return count, capped
}

// scaleFor 返回现货买单要缩小到的比例：现货钱包可用资金够用时返回 nil（不缩小），不够按比例缩小，没有资金时为 0。
func scaleFor(available, requirement shared.Decimal) *shared.Decimal {
	if requirement.Cmp(shared.Zero()) <= 0 || available.Cmp(requirement) >= 0 {
		return nil
	}
	ratio := shared.Zero()
	if available.Cmp(shared.Zero()) > 0 {
		// 向下取到 8 位小数：不取整的有理数比例会让缩小后的数量带分数，写不进 blocked_targets。
		ratio = available.Div(requirement).RoundDown(8)
	}
	return &ratio
}

// trailingHardFailures 返回末尾连续的硬失败次数（被拒绝、余额不足、交易所记录显示失败）和最近一次的原因。
// 限频没发出请求、宽限期后没找到记录的失败不计；遇到完成的记录就停。
func trailingHardFailures(history []store.WalletTransferRecord) (int, string) {
	count, last := 0, ""
	for index := len(history) - 1; index >= 0; index-- {
		row := history[index]
		if row.State == store.TransferStateCompleted {
			break
		}
		if row.State != store.TransferStateFailed || strings.HasPrefix(row.LastError, softNotSent) || strings.HasPrefix(row.LastError, softNotArrive) {
			continue
		}
		count++
		if last == "" {
			last = row.LastError
		}
	}
	return count, last
}

// buyRequirement 是这些现货买单需要的资金：每个标的 数量 × 卖一价（缺失时用最新价）求和，再乘 1.02。
func (s *Service) buyRequirement(ctx context.Context, adapter execution.ExecutionAdapter, buys []Buy) (shared.Decimal, error) {
	total := shared.Zero()
	for _, buy := range buys {
		quote, err := adapter.GetQuote(ctx, exchange.NewSymbolKey(exchange.MarketType(buy.Instrument.MarketType), buy.Instrument.ExchangeSymbol))
		if err != nil {
			return shared.Zero(), fmt.Errorf("取 %s 报价：%w", buy.Instrument.InstrumentID, err)
		}
		price := quote.Ask
		if price.Cmp(shared.Zero()) <= 0 {
			price = quote.Last
		}
		if price.Cmp(shared.Zero()) <= 0 {
			return shared.Zero(), fmt.Errorf("%s 没有可用报价", buy.Instrument.InstrumentID)
		}
		total = total.Add(buy.Quantity.Mul(price))
	}
	return total.Mul(dec(buyMarginRatio)), nil
}

func walletAvailable(snapshot exchange.AccountSnapshot, wallet exchange.Wallet, asset string) shared.Decimal {
	total := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Wallet == wallet && strings.EqualFold(balance.Asset, asset) {
			total = total.Add(balance.Available)
		}
	}
	return total
}

// walletTransferable 是钱包里资产最多能划转出去的数量（币安合约钱包的 maxWithdrawAmount）。
func walletTransferable(snapshot exchange.AccountSnapshot, wallet exchange.Wallet, asset string) shared.Decimal {
	total := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Wallet == wallet && strings.EqualFold(balance.Asset, asset) {
			total = total.Add(balance.Transferable)
		}
	}
	return total
}

func (s *Service) alertShortfallOnce(account store.TradingAccountRecord, targetID string, requirement, usable shared.Decimal) {
	detail := fmt.Sprintf("%s：现货买入需要 %s，合约钱包可划转的资金不足，只能用 %s，买单按比例缩小", account.Name, requirement.String(), usable.String())
	// 关注项用固定文字，需要多少、能用多少只写日志。
	s.alertOnceWithLabel(telemetry.AttentionTransferShortfall, targetID, account.TradingAccountID, detail,
		account.Name+"：合约钱包可划转的资金不足，现货买入被缩小或挡下")
}

// settleAccountPending 把这个账户未结（PENDING）的划转记录全部对账一遍，不论属于哪个目标、哪个方向。
// 返回窗口内的全部记录（含刚对账更新的）、仍未结的记录；查不到交易所划转记录时返回错误（调用方计数、退避、告警）。
func (s *Service) settleAccountPending(ctx context.Context, spaceID string, account store.TradingAccountRecord, transferer execution.WalletTransferer) ([]store.WalletTransferRecord, []store.WalletTransferRecord, error) {
	now := s.now()
	rows, err := s.Store.ListWalletTransfersByAccount(ctx, spaceID, account.TradingAccountID, now.Add(-accountWindow).UnixMilli(), now.Add(time.Hour).UnixMilli())
	if err != nil {
		return nil, nil, err
	}
	var pending []store.WalletTransferRecord
	var listErr error
	for index, row := range rows {
		if row.State != store.TransferStatePending {
			continue
		}
		resolved, err := s.reconcile(ctx, spaceID, row, rows, transferer)
		if err != nil {
			listErr = errors.Join(listErr, err)
		}
		rows[index] = resolved
		if resolved.State == store.TransferStatePending {
			pending = append(pending, resolved)
			if now.Sub(time.UnixMilli(resolved.RequestedAt)) >= pendingAlertAfter {
				s.alertOnce(telemetry.AttentionTransferFailing, resolved.TargetID, account.TradingAccountID,
					fmt.Sprintf("%s：一笔划转（%s %s，划转编号 %s）超过 %d 分钟还没有确认，这个账户不会再发起新的划转；"+
						"请到交易所核对划转记录，再用 ResolveWalletTransfer 人工结束这条记录（到账填 COMPLETED，没到账填 FAILED）",
						account.Name, resolved.Amount, resolved.Asset, resolved.TransferID, int(pendingAlertAfter/time.Minute)))
			}
		}
	}
	return rows, pending, listErr
}

// waitingForSnapshot 报告最近一笔完成的“划进现货钱包”的划转，现货钱包的存储快照是否还没追上：
// 现货钱包的快照更新时间（交易所时钟）不早于划转的完成时间（交易所记录的时间）才算追上，没追上时请求同步。
// 超过 snapshotGrace 仍没追上就放行：买入前的余额是现查的，存储快照稍旧最多让校验多拦一次或交易所拒单，不会多花钱。
func (s *Service) waitingForSnapshot(ctx context.Context, spaceID string, account store.TradingAccountRecord, rows []store.WalletTransferRecord) bool {
	var latest store.WalletTransferRecord
	for _, row := range rows {
		if row.State == store.TransferStateCompleted && row.Direction == store.TransferToSpot && row.CompletedAt > latest.CompletedAt {
			latest = row
		}
	}
	if latest.TransferID == "" || s.now().Sub(time.UnixMilli(latest.CompletedAt)) >= snapshotGrace {
		return false
	}
	current, err := s.Store.GetTradingAccount(ctx, spaceID, account.TradingAccountID)
	if err != nil {
		return true
	}
	// 现货钱包自己的更新时间；没有记录时退回整体的快照时间。
	updated := current.SnapshotSourceTime
	if at, ok := current.Snapshot.WalletUpdatedAt[string(exchange.WalletSpot)]; ok && at > 0 {
		updated = at
	}
	if updated >= latest.CompletedAt {
		return false
	}
	if s.RequestSync != nil {
		s.RequestSync(account.TradingAccountID)
	}
	return true
}

// reconcile 对账一条 PENDING 的划转记录；all 是这个账户窗口内的全部记录（用来排除已被别的记录认领的流水号）。
// 查不了交易所的划转记录（限频、网络）时记录保持 PENDING 并返回错误，不重试划转。
func (s *Service) reconcile(ctx context.Context, spaceID string, row store.WalletTransferRecord, all []store.WalletTransferRecord, transferer execution.WalletTransferer) (store.WalletTransferRecord, error) {
	since := time.UnixMilli(row.RequestedAt).Add(-historyLookback)
	records, err := transferer.ListTransfers(ctx, since)
	if err != nil {
		return row, err
	}
	claimed := map[string]bool{}
	for _, other := range all {
		if other.TransferID != row.TransferID && other.ExchangeTransferID != "" {
			claimed[other.ExchangeTransferID] = true
		}
	}
	var match *execution.TransferRecord
	for index := range records {
		record := records[index]
		if row.ExchangeTransferID != "" {
			if record.ExchangeTransferID == row.ExchangeTransferID {
				match = &record
				break
			}
			continue
		}
		if matchesRow(record, row) && !claimed[record.ExchangeTransferID] {
			match = &record
			break
		}
	}
	if match == nil {
		if row.ExchangeTransferID == "" && s.now().Sub(time.UnixMilli(row.RequestedAt)) >= confirmGrace {
			// 过了对账宽限期，交易所的划转记录里仍然没有：确认没到账，这条记录作废，允许重试（不计入连续失败）。
			return s.finish(ctx, spaceID, row, "", store.TransferStateFailed, softNotArrive+"交易所划转记录里没有找到这笔划转，确认没到账", 0)
		}
		return row, nil
	}
	switch match.Status {
	case execution.TransferConfirmed:
		// 完成时间用交易所记录的时间：现货钱包的快照要不早于它才算追上。
		completedAt := match.At.UnixMilli()
		if completedAt <= 0 {
			completedAt = s.now().UnixMilli()
		}
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStateCompleted, "", completedAt)
	case execution.TransferFailed:
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStateFailed, "交易所划转记录显示失败", 0)
	}
	// 交易所记录还是 PENDING：记下流水号，继续等。PENDING 不等于没到账，不能重试。
	if row.ExchangeTransferID == "" {
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStatePending, "", 0)
	}
	return row, nil
}

// matchesRow 报告交易所划转记录是否就是这条本地记录：方向、资产、金额一致，发生时间不早于请求时间（容许时钟偏差）。
func matchesRow(record execution.TransferRecord, row store.WalletTransferRecord) bool {
	wantFrom, wantTo := exchange.WalletFutures, exchange.WalletSpot
	if row.Direction == store.TransferToFutures {
		wantFrom, wantTo = exchange.WalletSpot, exchange.WalletFutures
	}
	amount, err := shared.ParseDecimal(row.Amount)
	if err != nil {
		return false
	}
	return record.From == wantFrom && record.To == wantTo && strings.EqualFold(record.Asset, row.Asset) &&
		record.Amount.Cmp(amount) == 0 &&
		!record.At.Before(time.UnixMilli(row.RequestedAt).Add(-historyLookback))
}

// finish 把一条 PENDING 的记录推进到新状态。记录已经不是 PENDING（被对账的另一轮或人工结束抢先处理了）不是错误：
// 原样返回，下一轮读到最新状态；不能把它当成查询失败去退避、告警。
func (s *Service) finish(ctx context.Context, spaceID string, row store.WalletTransferRecord, exchangeID, state, lastError string, completedAt int64) (store.WalletTransferRecord, error) {
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateWalletTransfer(spaceID, row.TransferID, exchangeID, state, lastError, completedAt)
	}); err != nil {
		if errors.Is(err, store.ErrNotPending) {
			return row, nil
		}
		return row, err
	}
	row.ExchangeTransferID, row.State, row.LastError, row.CompletedAt = exchangeID, state, lastError, completedAt
	return row, nil
}

// transferStart 是一次划转请求的结果：
//   - started：已发出或结果不明（等对账），这一轮不买；
//   - soft：请求确定没有被受理（限频、本地限流暂停、写记录失败），不是划转本身的问题，按退避等待后重试；
//   - 都不是：确定被拒绝或失败（硬失败），failure 是给人看的原因。
type transferStart struct {
	started bool
	soft    bool
	failure string
}

// startTransfer 先写 PENDING 记录，再请求交易所：
//   - 成功：记下流水号，状态仍是 PENDING，下一轮对账确认到账；
//   - 确定没有受理：限频（不计入连续失败）记 FAILED 并带 [未发出] 标记；被拒绝、余额不足等是硬失败；
//   - 结果不明（超时、传输错误）：记录保持 PENDING 且没有流水号，下一轮靠交易所划转记录对账，确认没到账才重试。
//
// refKey 是本地引用号的前缀：目标划转用 TargetID，风控归集（TargetID 为空串）用这次风控事件的幂等键，
// 保证（账户、引用号）唯一约束能区分两次风控归集。
//
// capped 为真表示这笔划转被合约钱包的可划转额度封顶（金额小于需要的金额）：写在记录上，完成后这个目标不再追加划转。
func (s *Service) startTransfer(
	ctx context.Context,
	spaceID string,
	targetRecord store.LogicalAccountTargetRecord,
	refKey string,
	account store.TradingAccountRecord,
	direction, asset string,
	amount shared.Decimal,
	capped bool,
	attempt int,
	transferer execution.WalletTransferer,
	from, to exchange.Wallet,
) transferStart {
	now := s.now()
	row := store.WalletTransferRecord{
		SpaceID: spaceID, TransferID: s.newID(), TradingAccountID: account.TradingAccountID,
		LogicalAccountID: targetRecord.LogicalAccountID, TargetID: targetRecord.TargetID, Direction: direction,
		Asset: asset, Amount: amount.RoundDown(8).String(), Capped: capped,
		ClientRef: fmt.Sprintf("%s:%s:%d", refKey, direction, attempt+1), State: store.TransferStatePending,
		RequestedAt: now.UnixMilli(),
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.InsertWalletTransfer(row) }); err != nil {
		return transferStart{soft: true, failure: "写划转记录失败：" + err.Error()}
	}
	receipt, err := transferer.Transfer(ctx, from, to, asset, amount.RoundDown(8), row.ClientRef)
	if err != nil {
		summary := truncate(exchange.FailureSummary(err), 200)
		switch {
		case exchange.NotAccepted(err):
			if _, finishErr := s.finish(ctx, spaceID, row, "", store.TransferStateFailed, softNotSent+summary, 0); finishErr != nil {
				log.Warnf("交易划转：记录限频失败：%v", finishErr)
			}
			return transferStart{soft: true, failure: summary}
		case definitelyRejected(err):
			if _, finishErr := s.finish(ctx, spaceID, row, "", store.TransferStateFailed, summary, 0); finishErr != nil {
				log.Warnf("交易划转：记录划转失败出错：%v", finishErr)
			}
			return transferStart{failure: summary}
		}
		// 结果不明：保持 PENDING，下一轮对账。
		log.Warnf("交易划转：账户 %s 划转 %s %s 结果不明，等待对账：%s", account.Name, amount.String(), asset, summary)
		return transferStart{started: true}
	}
	if _, err := s.finish(ctx, spaceID, row, receipt.ExchangeTransferID, store.TransferStatePending, "", 0); err != nil {
		// 已经划出去但没记上流水号：下一轮靠金额和时间窗口对账认领，不会重复划。
		log.Warnf("交易划转：记录流水号失败，等待对账认领：%v", err)
	}
	return transferStart{started: true}
}

// definitelyRejected 报告划转请求是否确定被交易所拒绝（没有执行）；超时和传输错误结果不明。
func definitelyRejected(err error) bool {
	return exchange.IsKind(err, exchange.ErrorRejected) || exchange.IsKind(err, exchange.ErrorInsufficientBalance) ||
		exchange.IsKind(err, exchange.ErrorAuthentication)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Collect 实现第 6 阶段：目标执行完毕后，把现货钱包里多出来的结算资产归集回合约钱包。
// 现货钱包只留 BNB 补买所需的 USDT（开启 BNB 保留时，现货钱包应持有保留金额的一半，缺的部分折成 USDT 留下）和 1U；
// 多出来的低于 max(100U, 权益 × 1%) 不划，避免 1 分钟级策略每期来回划转。每一轮都会调用：
// 这个目标已经归集过（有记录）就不再划；这个目标未结的记录对账到完成为止；别的目标留下的未结记录不挡本目标的终态，
// 但账户有未结划转时不发起新的归集。归集是收尾动作，任何问题都只告警、跳过，不挡住目标定终态。
func (s *Service) Collect(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (bool, error) {
	return s.collect(ctx, spaceID, targetRecord, "", accounts)
}

// CollectForRisk 是风控（保证金率监控）触发后的归集：没有目标，TargetID 为空串，一次风控事件由幂等键标识
// （风控减仓的动作号 margin-<组合>-<时间>）。同一个幂等键最多归集一次，未结的记录对账到完成；
// 不同幂等键是不同的风控事件，各自归集，不会被上一次的记录当成“已经做过”。每一轮都会调用，实现幂等。
//
// 风控归集不沿用目标归集“收尾吞错”的语义（那是收尾动作，失败了留给下一份目标）：风控触发后卖出所得留在现货钱包补不到合约保证金，
// 必须做成。所以出错（查余额限频、划转没发出、被拒绝）返回 error，调用方下一轮重试；账户上有别的未结划转返回 pending=true；
// 只有真的没有可归集的资金，或这个幂等键的归集已经完成，才返回 (false, nil)。限频、没到账这类软失败的记录不算“做过”，
// 重试用新的本地引用号（<幂等键>:TO_FUTURES:<第几次>），不会撞唯一约束；被交易所确定拒绝连续 3 次后不再自动重试（告警一次，仍返回 error）。
func (s *Service) CollectForRisk(ctx context.Context, spaceID, logicalAccountID, idempotencyKey string, accounts []store.TradingAccountRecord) (bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return false, errors.New("风控归集需要幂等键")
	}
	return s.collect(ctx, spaceID, store.LogicalAccountTargetRecord{SpaceID: spaceID, LogicalAccountID: logicalAccountID}, idempotencyKey, accounts)
}

// collect 是目标归集和风控归集共用的实现：key 为空按目标标识一次归集，否则按幂等键标识（TargetID 为空串）。
func (s *Service) collect(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, key string, accounts []store.TradingAccountRecord) (bool, error) {
	pending := false
	alertID := targetRecord.TargetID
	if key != "" {
		alertID = key
	}
	var failures []error
	for _, account := range accounts {
		accountPending, err := s.collectForAccount(ctx, spaceID, targetRecord, key, account)
		if err != nil {
			s.alertOnce(telemetry.AttentionTransferFailing, alertID, account.TradingAccountID,
				fmt.Sprintf("%s：归集现货钱包的资金失败：%s", account.Name, truncate(exchange.FailureSummary(err), 200)))
			if key != "" {
				failures = append(failures, fmt.Errorf("账户 %s：%w", account.Name, err))
			}
			continue
		}
		pending = pending || accountPending
	}
	// 目标归集是收尾动作，失败只告警、跳过；风控归集把错误交给调用方，下一轮重试。
	return pending, errors.Join(failures...)
}

func (s *Service) collectForAccount(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, key string, account store.TradingAccountRecord) (bool, error) {
	// 这一次归集的记录：目标归集按 TargetID，风控归集（TargetID 为空串）按本地引用号里的幂等键。
	owns := func(row store.WalletTransferRecord) bool {
		if row.Direction != store.TransferToFutures {
			return false
		}
		if key == "" {
			return row.TargetID == targetRecord.TargetID
		}
		return row.TargetID == "" && strings.HasPrefix(row.ClientRef, key+":")
	}
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		return false, err
	}
	rows, pendingRows, err := s.settleAccountPending(ctx, spaceID, account, transferer)
	if err != nil {
		return false, err
	}
	for _, row := range pendingRows {
		if owns(row) {
			return true, nil
		}
	}
	if len(pendingRows) > 0 {
		// 账户上有别的未结划转：不发起新的归集。目标归集是收尾动作，不挡目标定终态；风控归集必须做成，等它结束后再来。
		return key != "", nil
	}
	attempt := 0
	hardFailures := 0
	for _, row := range rows {
		if !owns(row) {
			continue
		}
		if key == "" {
			// 这个目标已经归集过（或失败过）：每个目标最多归集一次，失败的留给下一份目标。
			return false, nil
		}
		attempt++
		switch {
		case row.State == store.TransferStateCompleted:
			return false, nil // 这次风控事件已经归集完成
		case row.State == store.TransferStateFailed && !strings.HasPrefix(row.LastError, softNotSent) && !strings.HasPrefix(row.LastError, softNotArrive):
			hardFailures++
		}
	}
	if hardFailures >= maxTransferFailures {
		return false, fmt.Errorf("风控归集被交易所拒绝 %d 次，不再自动重试，请手动把现货钱包的资金划回合约钱包", hardFailures)
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return false, err
	}
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	keep, ok, err := s.collectKeep(ctx, spaceID, targetRecord, key, account, adapter, snapshot)
	if err == nil && !ok && key != "" {
		err = errors.New("不知道现货钱包该留多少资金（资金设置或 BNB 价格读不出来），风控归集暂时不能做")
	}
	if err != nil || !ok {
		return false, err
	}
	excess := spotAvailable.Sub(keep).RoundDown(8)
	threshold, err := s.collectThreshold(ctx, spaceID, account, snapshot)
	if err != nil {
		return false, err
	}
	if excess.Cmp(threshold) < 0 {
		return false, nil
	}
	refKey := targetRecord.TargetID
	if key != "" {
		refKey = key
	}
	start := s.startTransfer(ctx, spaceID, targetRecord, refKey, account, store.TransferToFutures, asset, excess, false, attempt, transferer, exchange.WalletSpot, exchange.WalletFutures)
	if !start.started {
		return false, errors.New(start.failure)
	}
	return true, nil
}

// collectKeep 是归集后现货钱包里要留的 USDT：1U，加上 BNB 补买所需。开启 BNB 保留（金额 N）时，
// 普通账户的现货钱包应持有 N/2 的 BNB，当前 BNB 市值不够 N/2 的部分折成 USDT 留下。
// 资金设置读不出来、或 BNB 保留金额读不出来、或取不到 BNB 价格时，不知道该留多少，不归集并告警（ok=false）。
func (s *Service) collectKeep(
	ctx context.Context,
	spaceID string,
	targetRecord store.LogicalAccountTargetRecord,
	key string,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
	snapshot exchange.AccountSnapshot,
) (keep shared.Decimal, ok bool, err error) {
	alertID := targetRecord.TargetID
	if key != "" {
		alertID = key
	}
	keep = dec(collectKeepAmount)
	logical, err := s.Store.GetLogicalAccount(ctx, spaceID, targetRecord.LogicalAccountID)
	if err != nil {
		return keep, false, err
	}
	settings, _, parseErr := fundsettings.ParseLenient(logical.FundSettingsJSON)
	if parseErr != nil {
		s.alertOnce(telemetry.AttentionTransferFailing, alertID, account.TradingAccountID,
			fmt.Sprintf("%s：资金设置读不出来（%v），不知道现货钱包该留多少 BNB 补买资金，这次不归集", account.Name, parseErr))
		return keep, false, nil
	}
	if settings.BNBReserveUnreadable {
		s.alertOnce(telemetry.AttentionTransferFailing, alertID, account.TradingAccountID,
			account.Name+"：BNB 保留金额读不出来，不知道现货钱包该留多少补买资金，这次不归集")
		return keep, false, nil
	}
	if settings.BNBReserveAmount.Cmp(shared.Zero()) <= 0 {
		return keep, true, nil
	}
	target := settings.BNBReserveAmount.Div(dec("2"))
	held := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Wallet == exchange.WalletSpot && strings.EqualFold(balance.Asset, fundsettings.BNBAsset) {
			held = held.Add(balance.Total)
		}
	}
	if held.Cmp(shared.Zero()) > 0 {
		price, priceErr := adapter.GetReferencePrice(ctx, exchange.NewSymbolKey(exchange.MarketTypeSpot, fundsettings.BNBAsset+account.SettlementAsset))
		if priceErr != nil || price.Price.Cmp(shared.Zero()) <= 0 {
			return keep, false, fmt.Errorf("取 BNB 价格：%w", priceErr)
		}
		held = held.Mul(price.Price)
	}
	if shortage := target.Sub(held); shortage.Cmp(shared.Zero()) > 0 {
		keep = keep.Add(shortage)
	}
	return keep, true, nil
}

// collectThreshold 是归集阈值 max(100U, 权益 × 1%)。权益取最近一次权益采样；没有采样时用两个钱包的结算资产合计估算。
func (s *Service) collectThreshold(ctx context.Context, spaceID string, account store.TradingAccountRecord, snapshot exchange.AccountSnapshot) (shared.Decimal, error) {
	threshold := dec(collectMinThreshold)
	equity := shared.Zero()
	now := s.now()
	points, err := s.Store.ListAccountEquityPoints(ctx, spaceID, account.TradingAccountID, now.Add(-time.Hour).Truncate(time.Minute).UnixMilli(), now.UnixMilli())
	if err != nil {
		return shared.Zero(), err
	}
	if len(points) > 0 {
		if parsed, parseErr := shared.ParseDecimal(points[len(points)-1].Equity); parseErr == nil {
			equity = parsed
		}
	}
	if equity.IsZero() {
		for _, balance := range snapshot.Balances {
			if strings.EqualFold(balance.Asset, account.SettlementAsset) {
				equity = equity.Add(balance.Total)
			}
		}
	}
	if scaled := equity.Mul(dec(collectEquityRatio)); scaled.Cmp(threshold) > 0 {
		threshold = scaled
	}
	return threshold, nil
}
