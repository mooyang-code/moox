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
	// queryFailureAlert 是同一个账户连续查询失败多少次后告警；重试间隔指数退避，最长 maxBackoff。
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
	// Pending 为真表示还在划转、对账或等现货钱包快照，本轮不进入现货买入。
	Pending bool
	// BuyScale 是各账户现货买入要缩小到的比例（键：执行账户 ID，0 到 1）；没有条目表示不缩小。
	BuyScale map[string]shared.Decimal
	// Reasons 是比例缩小或为 0 的原因（键：执行账户 ID），执行器写进 blocked_targets。
	Reasons map[string]string
	// RetryAfter 是建议的下次收敛间隔（查询持续失败时指数退避）；0 表示用默认间隔。
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
	// queryFailures 是各账户连续查询（余额、报价、划转记录）失败的次数。
	queryFailures map[string]int
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
	telemetry.Note(kind, detail, s.now())
}

// queryFailed 记一次账户的查询失败，返回这一轮建议的退避时间（1 秒起指数退避，最长 30 秒）；连续失败到阈值告警。
func (s *Service) queryFailed(account store.TradingAccountRecord, err error) time.Duration {
	s.mu.Lock()
	if s.queryFailures == nil {
		s.queryFailures = map[string]int{}
	}
	s.queryFailures[account.TradingAccountID]++
	count := s.queryFailures[account.TradingAccountID]
	s.mu.Unlock()
	if count >= queryFailureAlert {
		log.Warnf("交易划转：账户 %s 连续 %d 次查询失败：%v", account.Name, count, err)
		telemetry.Note(telemetry.AttentionTransferFailing,
			fmt.Sprintf("%s：划转前的余额或划转记录连续 %d 次查询失败", account.Name, count), s.now())
	}
	backoff := time.Second << min(count-1, 5)
	return min(backoff, maxBackoff)
}

func (s *Service) queryOK(accountID string) {
	s.mu.Lock()
	delete(s.queryFailures, accountID)
	s.mu.Unlock()
}

// accountResult 是一个账户在第 4 阶段这一轮的结果。
type accountResult struct {
	pending    bool
	scale      *shared.Decimal
	reason     string
	retryAfter time.Duration
}

// BeforeSpotBuy 实现第 4 阶段。对每个账户：
//  1. 先把这个账户所有未结的划转记录（不论哪个目标）对账完；有未结的就等待，不发起新划转（目标 1 超时未到账时目标 2 到达，
//     不会按还没变化的余额再划一笔）；
//  2. 现查现货、合约两个钱包的余额（不用缓存），划转金额 = max(0, 买单名义总额 × 1.02 − 现货钱包可用 USDT)，
//     不超过合约钱包可划转额度（maxWithdrawAmount）× 0.99；不需要划转就直接放行，不受连续失败次数影响；
//  3. 同一个目标已经有完成的划转后不再划转，只按比例缩小买单（不会把合约钱包留的 1% 也用掉）；
//  4. 额度不足、划转连续失败、不支持划转：买单比例设 0 或按比例缩小，原因写明并告警；一个账户出问题不影响同组合其他账户。
func (s *Service) BeforeSpotBuy(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, buys []Buy) (Outcome, error) {
	outcome := Outcome{BuyScale: map[string]shared.Decimal{}, Reasons: map[string]string{}}
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
		outcome.Pending = outcome.Pending || result.pending
		outcome.RetryAfter = max(outcome.RetryAfter, result.retryAfter)
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
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		reason := fmt.Sprintf("账户钱包划转不可用：%v", err)
		s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID, account.Name+"："+reason)
		return accountResult{scale: &zero, reason: reason}
	}
	// 1. 这个账户所有未结的划转（不论目标）。
	rows, pendingRows, listErr := s.settleAccountPending(ctx, spaceID, account, transferer)
	if listErr != nil {
		return accountResult{pending: true, retryAfter: s.queryFailed(account, listErr)}
	}
	if len(pendingRows) > 0 {
		return accountResult{pending: true}
	}
	// 2. 现查报价和余额。
	requirement, err := s.buyRequirement(ctx, adapter, buys)
	if err != nil {
		return accountResult{pending: true, retryAfter: s.queryFailed(account, err)}
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return accountResult{pending: true, retryAfter: s.queryFailed(account, err)}
	}
	s.queryOK(account.TradingAccountID)
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	// 3. 刚完成的划转：等现货钱包的快照追上，校验和下单才看得到这笔资金。
	if s.waitingForSnapshot(ctx, spaceID, account, rows) {
		return accountResult{pending: true}
	}
	need := requirement.Sub(spotAvailable)
	if need.Cmp(dec(minTransferAmount)) < 0 {
		return accountResult{scale: scaleFor(spotAvailable, requirement)}
	}
	// 4. 需要划转。这个目标已经有完成的划转：不再划，只按比例缩小（本目标累计划转不会超过首次可划额度）。
	history, err := s.Store.ListWalletTransfersForTarget(ctx, spaceID, targetRecord.LogicalAccountID, targetRecord.TargetID, account.TradingAccountID, store.TransferToSpot)
	if err != nil {
		return accountResult{pending: true, retryAfter: s.queryFailed(account, err)}
	}
	for _, row := range history {
		if row.State == store.TransferStateCompleted {
			reason := "合约钱包可划转的资金已经划完，现货买入按现有资金缩小"
			s.alertShortfallOnce(account, targetRecord.TargetID, requirement, spotAvailable)
			return accountResult{scale: scaleFor(spotAvailable, requirement), reason: reason}
		}
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
		return accountResult{scale: scaleFor(spotAvailable, requirement), reason: "合约钱包可划转的资金不足，现货买入按现有资金缩小"}
	}
	if amount.Cmp(need) < 0 {
		s.alertShortfallOnce(account, targetRecord.TargetID, requirement, spotAvailable.Add(amount))
	}
	started, failure := s.startTransfer(ctx, spaceID, targetRecord, account, store.TransferToSpot, asset, amount, len(history), transferer, exchange.WalletFutures, exchange.WalletSpot)
	switch {
	case started:
		// 划转已发出（或结果不明、等对账），这一轮不买。
		return accountResult{pending: true}
	default:
		// 确定没有划成：这一轮这个账户不买，下一轮再试，连续硬失败到上限后不再重试。
		return accountResult{scale: &zero, reason: "合约钱包划转没有成功：" + failure}
	}
}

// scaleFor 返回现货买单要缩小到的比例：现货钱包可用资金够用时返回 nil（不缩小），不够按比例缩小，没有资金时为 0。
func scaleFor(available, requirement shared.Decimal) *shared.Decimal {
	if requirement.Cmp(shared.Zero()) <= 0 || available.Cmp(requirement) >= 0 {
		return nil
	}
	ratio := shared.Zero()
	if available.Cmp(shared.Zero()) > 0 {
		ratio = available.Div(requirement)
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
	s.alertOnce(telemetry.AttentionTransferShortfall, targetID, account.TradingAccountID, detail)
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
					fmt.Sprintf("%s：一笔划转（%s %s）超过 %d 分钟还没有确认，这个账户不会再发起新的划转，请人工核对交易所的划转记录",
						account.Name, resolved.Amount, resolved.Asset, int(pendingAlertAfter/time.Minute)))
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

func (s *Service) finish(ctx context.Context, spaceID string, row store.WalletTransferRecord, exchangeID, state, lastError string, completedAt int64) (store.WalletTransferRecord, error) {
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateWalletTransfer(spaceID, row.TransferID, exchangeID, state, lastError, completedAt)
	}); err != nil {
		return row, err
	}
	row.ExchangeTransferID, row.State, row.LastError, row.CompletedAt = exchangeID, state, lastError, completedAt
	return row, nil
}

// startTransfer 先写 PENDING 记录，再请求交易所。返回 started=true 表示划转已发出或结果不明（等对账）；
// started=false 表示确定没有划成，failure 是给人看的原因：
//   - 成功：记下流水号，状态仍是 PENDING，下一轮对账确认到账；
//   - 确定没有受理：限频（不计入连续失败）记 FAILED 并带 [未发出] 标记；被拒绝、余额不足等是硬失败；
//   - 结果不明（超时、传输错误）：记录保持 PENDING 且没有流水号，下一轮靠交易所划转记录对账，确认没到账才重试。
func (s *Service) startTransfer(
	ctx context.Context,
	spaceID string,
	targetRecord store.LogicalAccountTargetRecord,
	account store.TradingAccountRecord,
	direction, asset string,
	amount shared.Decimal,
	attempt int,
	transferer execution.WalletTransferer,
	from, to exchange.Wallet,
) (started bool, failure string) {
	now := s.now()
	row := store.WalletTransferRecord{
		SpaceID: spaceID, TransferID: s.newID(), TradingAccountID: account.TradingAccountID,
		LogicalAccountID: targetRecord.LogicalAccountID, TargetID: targetRecord.TargetID, Direction: direction,
		Asset: asset, Amount: amount.RoundDown(8).String(),
		ClientRef: fmt.Sprintf("%s:%s:%d", targetRecord.TargetID, direction, attempt+1), State: store.TransferStatePending,
		RequestedAt: now.UnixMilli(),
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.InsertWalletTransfer(row) }); err != nil {
		return false, "写划转记录失败：" + err.Error()
	}
	receipt, err := transferer.Transfer(ctx, from, to, asset, amount.RoundDown(8), row.ClientRef)
	if err != nil {
		summary := truncate(failureSummary(err), 200)
		switch {
		case exchange.NotAccepted(err):
			if _, finishErr := s.finish(ctx, spaceID, row, "", store.TransferStateFailed, softNotSent+summary, 0); finishErr != nil {
				log.Warnf("交易划转：记录限频失败：%v", finishErr)
			}
			return false, summary
		case definitelyRejected(err):
			if _, finishErr := s.finish(ctx, spaceID, row, "", store.TransferStateFailed, summary, 0); finishErr != nil {
				log.Warnf("交易划转：记录划转失败出错：%v", finishErr)
			}
			return false, summary
		}
		// 结果不明：保持 PENDING，下一轮对账。
		log.Warnf("交易划转：账户 %s 划转 %s %s 结果不明，等待对账：%s", account.Name, amount.String(), asset, summary)
		return true, ""
	}
	if _, err := s.finish(ctx, spaceID, row, receipt.ExchangeTransferID, store.TransferStatePending, "", 0); err != nil {
		// 已经划出去但没记上流水号：下一轮靠金额和时间窗口对账认领，不会重复划。
		log.Warnf("交易划转：记录流水号失败，等待对账认领：%v", err)
	}
	return true, ""
}

// failureSummary 是划转失败的简短说明：只含错误类别、状态和错误码，不含请求串和响应体原文。
func failureSummary(err error) string {
	var typed *exchange.Error
	if errors.As(err, &typed) {
		parts := []string{string(typed.Kind)}
		if typed.HTTPStatus != 0 {
			parts = append(parts, fmt.Sprintf("HTTP %d", typed.HTTPStatus))
		}
		if typed.Code != "" {
			parts = append(parts, "错误码 "+typed.Code)
		}
		return strings.Join(parts, "，")
	}
	return "没有得到交易所的明确答复"
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
	pending := false
	for _, account := range accounts {
		accountPending, err := s.collectForAccount(ctx, spaceID, targetRecord, account)
		if err != nil {
			s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID,
				fmt.Sprintf("%s：归集现货钱包的资金失败，已跳过：%s", account.Name, truncate(failureSummary(err), 200)))
			continue
		}
		pending = pending || accountPending
	}
	return pending, nil
}

func (s *Service) collectForAccount(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, account store.TradingAccountRecord) (bool, error) {
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		return false, err
	}
	rows, pendingRows, err := s.settleAccountPending(ctx, spaceID, account, transferer)
	if err != nil {
		return false, err
	}
	for _, row := range pendingRows {
		if row.TargetID == targetRecord.TargetID && row.Direction == store.TransferToFutures {
			return true, nil
		}
	}
	if len(pendingRows) > 0 {
		return false, nil
	}
	for _, row := range rows {
		if row.TargetID == targetRecord.TargetID && row.Direction == store.TransferToFutures {
			// 这个目标已经归集过（或失败过）：每个目标最多归集一次，失败的留给下一份目标。
			return false, nil
		}
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return false, err
	}
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	keep, ok, err := s.collectKeep(ctx, spaceID, targetRecord, account, adapter, snapshot)
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
	started, failure := s.startTransfer(ctx, spaceID, targetRecord, account, store.TransferToFutures, asset, excess, 0, transferer, exchange.WalletSpot, exchange.WalletFutures)
	if !started {
		return false, errors.New(failure)
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
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
	snapshot exchange.AccountSnapshot,
) (keep shared.Decimal, ok bool, err error) {
	keep = dec(collectKeepAmount)
	logical, err := s.Store.GetLogicalAccount(ctx, spaceID, targetRecord.LogicalAccountID)
	if err != nil {
		return keep, false, err
	}
	settings, _, parseErr := fundsettings.ParseLenient(logical.FundSettingsJSON)
	if parseErr != nil {
		s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID,
			fmt.Sprintf("%s：资金设置读不出来（%v），不知道现货钱包该留多少 BNB 补买资金，这次不归集", account.Name, parseErr))
		return keep, false, nil
	}
	if settings.BNBReserveUnreadable {
		s.alertOnce(telemetry.AttentionTransferFailing, targetRecord.TargetID, account.TradingAccountID,
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
