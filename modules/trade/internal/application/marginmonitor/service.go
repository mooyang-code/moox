// Package marginmonitor 是交易计划 4.7 的保证金率监控：币安实盘账户的保证金率跌到阈值以下时，暂停组合并发起按比例减仓（REDUCE），
// 普通账户再把现货钱包的 USDT 归集回合约钱包，前后都告警，由用户确认安全后手动恢复。
//
// 口径（币安生产环境、已启用且就绪的实盘永续账户）：
//   - 普通账户（BINANCE_STANDARD）：交易所总权益 ÷ 合约持仓名义，不高于阈值（默认 0.5）时触发。总权益取账户快照的 ExchangeEquity：
//     现货快照不填这个字段，多市场合并时相加，所以普通账户上它就是合约接口的 totalMarginBalance（钱包余额 + 未实现盈亏 + 多资产模式下的抵押，
//     正是币安决定强平的口径，保证金币手工划到合约钱包的部分也算在内）。持仓名义 = Σ 持仓数量绝对值 × 标记价（没有标记价用开仓均价）。
//     计划 4.6 的“策略权益不用交易所总权益”指的是换算目标的权益，不是这里的风控读数。
//   - 统一账户（BINANCE_PORTFOLIO_MARGIN）：uniMMR = 总权益 ÷ 维持保证金，低于阈值（默认 1.5）时触发；总权益取 ExchangeEquity，
//     维持保证金取快照里的 MaintenanceMargin。
//   - 分母为零或数据缺失（没有持仓、维持保证金或总权益没填、快照太久没更新）一律不触发：数据不全时不能凭空减仓。
//   - OKX 暂不接入：官方样例里账户级的维持保证金在 mmr、adjEq，币种明细里的 mmr 和 mgnRatio 是空串，现有快照读不对，先不监控、告警一次；
//     账户类型还没有检测写入（依赖交易所检测任务 T5）的实盘永续账户同样不监控、告警一次。
//   - 测试网和模拟盘不监控。
package marginmonitor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// Reduce 发起按比例减仓（先暂停组合、撤全部订单，再把现货和合约同比例减掉）。
type Reduce func(ctx context.Context, spaceID, actionID, logicalAccountID string, ratio shared.Decimal, reason string) (store.OperatorActionRecord, error)

// Pause 只暂停组合（有下架清仓在跑时减仓排不进去，先把组合停住）。
type Pause func(ctx context.Context, spaceID, logicalAccountID, reason string) error

// Collect 把普通账户现货钱包里的资金归集回合约钱包；pending 为真表示还没完成，下一轮再调（实现须幂等）。
type Collect func(ctx context.Context, spaceID, logicalAccountID, actionID string, accounts []store.TradingAccountRecord) (pending bool, err error)

// ErrTransferUnavailable 表示还没有接入钱包划转：监控只告警一次，请用户手动把现货钱包的资金划回合约钱包。
var ErrTransferUnavailable = errors.New("钱包划转还没有接入")

// ActionIDPrefix 是风控发起的减仓动作号的前缀，用来在操作记录里认出它们。
const ActionIDPrefix = "margin-"

// 默认的快照最长允许年龄：账户同步间隔是 30 秒，超过这个时间没更新说明数据不可信，不据此减仓。
const defaultMaxSnapshotAge = 3 * time.Minute

// activeSlack 是“监控看到组合在运行”要晚于上一次风控减仓多久才算用户恢复过：操作记录的创建时间只精确到秒。
const activeSlack = 2 * time.Second

type Service struct {
	Store   *store.Store
	Reduce  Reduce
	Pause   Pause
	Collect Collect
	Now     func() time.Time
	// MaxSnapshotAge 是账户快照的最长允许年龄，默认 3 分钟。
	MaxSnapshotAge time.Duration

	mu        sync.Mutex
	collected map[string]struct{}
	warned    map[string]struct{}
	// lastActive 是监控最近一次看到组合处于运行状态的时间（进程内）：风控减仓之后，要看到组合又运行过才会再次触发，
	// 用户人工操作改写暂停原因也不会让监控再减一次。进程重启后清零，对“暂停着且减过仓”的组合偏保守（不触发，等用户恢复）。
	lastActive map[string]time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) maxSnapshotAge() time.Duration {
	if s.MaxSnapshotAge > 0 {
		return s.MaxSnapshotAge
	}
	return defaultMaxSnapshotAge
}

func (s *Service) warnOnce(key, message string) {
	s.mu.Lock()
	if s.warned == nil || len(s.warned) >= 1000 {
		s.warned = make(map[string]struct{})
	}
	_, repeated := s.warned[key]
	s.warned[key] = struct{}{}
	s.mu.Unlock()
	if !repeated {
		log.Warnf("交易关注项：%s", message)
	}
}

// Reading 是一个执行账户这一轮的保证金率读数。
type Reading struct {
	AccountName string
	// Unified 为真表示统一账户口径（总权益 ÷ 维持保证金），否则是普通账户口径（总权益 ÷ 合约持仓名义）。
	Unified   bool
	Value     shared.Decimal
	Threshold shared.Decimal
	Triggered bool
}

// Describe 只用在日志里（比值是变化的数字，不进暂停原因和告警详情）。
func (r Reading) Describe() string {
	if r.Unified {
		return fmt.Sprintf("%s：uniMMR %.4f 低于阈值 %s", r.AccountName, r.Value.Float64(), r.Threshold)
	}
	return fmt.Sprintf("%s：总权益 ÷ 持仓名义 = %.4f，不高于阈值 %s", r.AccountName, r.Value.Float64(), r.Threshold)
}

// inScope 报告账户是否是监控关心的账户：实盘生产环境、已启用、开通了永续（不管类型是否已知、是否就绪）。
func inScope(account store.TradingAccountRecord) bool {
	return account.ExecutionMode == string(exchange.ExecutionModeLive) && account.Environment == string(exchange.AccountEnvironmentProduction) &&
		account.Status == string(exchange.AccountStatusEnabled) && account.SupportsMarket(string(exchange.MarketTypeSwap))
}

// Applies 报告账户是否在监控范围内：币安的普通账户和统一账户，已就绪。
func Applies(account store.TradingAccountRecord) bool {
	if !inScope(account) || !account.Ready {
		return false
	}
	switch exchange.AccountMode(account.AccountMode) {
	case exchange.AccountModeBinanceStandard, exchange.AccountModeBinancePortfolioMargin:
		return account.Exchange == string(exchange.ExchangeBinance)
	}
	return false
}

// unsupportedNote 说明范围内的账户为什么没有被监控（固定文字，没有被跳过时为空串）。
func unsupportedNote(account store.TradingAccountRecord) string {
	if !inScope(account) {
		return ""
	}
	switch {
	case account.Exchange == string(exchange.ExchangeOKX):
		return "OKX 保证金率监控未接入：这个账户没有保证金率保护"
	case exchange.AccountMode(account.AccountMode) == exchange.AccountModeUnspecified:
		return "账户类型还没有检测写入（依赖交易所检测），保证金率监控对这个账户不生效"
	}
	return ""
}

// Evaluate 算出一个账户的保证金率读数；数据不全（分母为零、没有持仓、总权益没填）时 ok 为假，不触发。
func Evaluate(account store.TradingAccountRecord, positions []store.PositionRecord, settings fundsettings.Settings) (Reading, bool) {
	reading := Reading{AccountName: account.Name}
	equity := parseOrZero(account.Snapshot.ExchangeEquity)
	if equity.Cmp(shared.Zero()) <= 0 {
		return reading, false
	}
	if exchange.AccountMode(account.AccountMode) != exchange.AccountModeBinanceStandard {
		reading.Unified, reading.Threshold = true, settings.UnifiedMMRThreshold
		maintenance := parseOrZero(account.Snapshot.MaintenanceMargin)
		if maintenance.Cmp(shared.Zero()) <= 0 {
			return reading, false
		}
		reading.Value = equity.Div(maintenance)
		reading.Triggered = reading.Value.Cmp(reading.Threshold) < 0
		return reading, true
	}
	reading.Threshold = settings.MarginRatioThreshold
	notional := shared.Zero()
	for _, position := range positions {
		if position.MarketType != string(exchange.MarketTypeSwap) {
			continue
		}
		quantity := parseOrZero(position.SignedQuantity).Abs()
		if quantity.IsZero() {
			continue
		}
		price := parseOrZero(position.MarkPrice)
		if price.Cmp(shared.Zero()) <= 0 {
			price = parseOrZero(position.EntryPrice)
		}
		notional = notional.Add(quantity.Mul(price))
	}
	if notional.Cmp(shared.Zero()) <= 0 {
		return reading, false
	}
	reading.Value = equity.Div(notional)
	reading.Triggered = reading.Value.Cmp(reading.Threshold) <= 0
	return reading, true
}

func parseOrZero(raw string) shared.Decimal {
	if raw == "" {
		return shared.Zero()
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}

// RunAll 检查所有空间的所有组合账户；单个组合失败不影响其他组合。
func (s *Service) RunAll(ctx context.Context) error {
	if s == nil || s.Store == nil {
		return errors.New("trade marginmonitor: service is not configured")
	}
	accounts, err := s.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	spaces := make(map[string]struct{})
	for _, account := range accounts {
		spaces[account.SpaceID] = struct{}{}
	}
	ordered := make([]string, 0, len(spaces))
	for spaceID := range spaces {
		ordered = append(ordered, spaceID)
	}
	sort.Strings(ordered)
	var runErrors []error
	for _, spaceID := range ordered {
		logicals, err := s.Store.ListLogicalAccounts(ctx, spaceID)
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		for _, logical := range logicals {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.RunLogicalAccount(ctx, logical); err != nil {
				runErrors = append(runErrors, fmt.Errorf("组合 %s：%w", logical.LogicalAccountID, err))
			}
		}
	}
	return errors.Join(runErrors...)
}

// RunLogicalAccount 对一个组合账户检查一遍：先把上次触发后还没做完的归集做完，再判断这一轮要不要触发。
func (s *Service) RunLogicalAccount(ctx context.Context, logical store.LogicalAccountRecord) error {
	if logical.ExecutionMode != string(exchange.ExecutionModeLive) {
		return nil
	}
	settings, err := s.Store.ResolveFundSettings(logical)
	if err != nil {
		// 排除字段读不出来：设置不可信，监控也无法知道阈值，告警后跳过（执行器同样不下单）。
		s.warnOnce("settings|"+logical.LogicalAccountID, fmt.Sprintf("组合 %s 的资金设置无法解析，保证金率监控跳过：%v", logical.LogicalAccountID, err))
		return nil
	}
	if settings.MarginMonitorDisabled {
		return nil
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return err
	}
	accounts := make([]store.TradingAccountRecord, 0, len(members))
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return err
		}
		if note := unsupportedNote(account); note != "" {
			s.warnOnce("unsupported|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s：%s", account.Name, note))
		}
		accounts = append(accounts, account)
	}
	if err := s.collectAfterReduce(ctx, logical, accounts); err != nil {
		log.Warnf("交易关注项：组合 %s 触发风控后的资金归集失败，下一轮重试：%v", logical.LogicalAccountID, err)
	}
	if logical.AutomationState == "ACTIVE" {
		s.markActive(logical.LogicalAccountID)
	}
	// 整组清仓和减仓在跑时，它们自己会暂停组合，这一轮不叠加；下架清仓不暂停组合，下面照常触发（减仓排不进去时先暂停）。
	running, err := s.Store.ListRunningOperatorActions(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return err
	}
	for _, action := range running {
		if action.ActionType == string(operatordomain.ActionFlatten) || action.ActionType == string(operatordomain.ActionReduce) {
			return nil
		}
	}
	// 上一次风控减仓之后，要看到组合又运行过（用户确认安全后恢复）才会再触发：用户在暂停期间人工下单、撤单、清仓改写了暂停原因，
	// 比值仍低也不会再自动减一次。
	last, err := s.lastMarginAction(ctx, logical)
	if err != nil {
		return err
	}
	if last != nil && !s.activeSince(logical.LogicalAccountID, last.CreatedAt.Add(activeSlack)) {
		return nil
	}
	var triggered []Reading
	for _, account := range accounts {
		if !Applies(account) {
			continue
		}
		if age := s.now().Sub(time.UnixMilli(account.LastSyncAt)); account.LastSyncAt <= 0 || age > s.maxSnapshotAge() {
			s.warnOnce("stale|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 的快照太久没有更新，保证金率监控这一轮跳过", account.Name))
			continue
		}
		positions, err := s.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, exchange.SymbolKey{})
		if err != nil {
			return err
		}
		if reading, ok := Evaluate(account, positions, settings); ok && reading.Triggered {
			triggered = append(triggered, reading)
		}
	}
	if len(triggered) == 0 {
		return nil
	}
	return s.trigger(ctx, logical, settings, triggered)
}

func (s *Service) markActive(logicalAccountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastActive == nil {
		s.lastActive = make(map[string]time.Time)
	}
	s.lastActive[logicalAccountID] = s.now()
}

func (s *Service) activeSince(logicalAccountID string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActive[logicalAccountID].After(at)
}

// lastMarginAction 返回组合最近一次风控发起的减仓动作，没有则为 nil。
func (s *Service) lastMarginAction(ctx context.Context, logical store.LogicalAccountRecord) (*store.OperatorActionRecord, error) {
	actions, _, err := s.Store.ListOperatorActions(ctx, logical.SpaceID, store.OperatorActionQuery{
		LogicalAccountID: logical.LogicalAccountID, ActionType: string(operatordomain.ActionReduce), Limit: 20,
	})
	if err != nil {
		return nil, err
	}
	for i := range actions {
		if len(actions[i].ActionID) >= len(ActionIDPrefix) && actions[i].ActionID[:len(ActionIDPrefix)] == ActionIDPrefix {
			return &actions[i], nil
		}
	}
	return nil, nil
}

func (s *Service) trigger(ctx context.Context, logical store.LogicalAccountRecord, settings fundsettings.Settings, readings []Reading) error {
	detail := ""
	for _, reading := range readings {
		if detail != "" {
			detail += "；"
		}
		detail += reading.Describe()
	}
	reason := store.MarginPauseReason()
	actionID := ActionIDPrefix + logical.LogicalAccountID + "-" + s.now().UTC().Format("20060102T150405")
	_, err := s.Reduce(ctx, logical.SpaceID, actionID, logical.LogicalAccountID, settings.MarginReduceRatio, reason)
	if errors.Is(err, operatorapp.ErrLogicalAccountBusy) {
		// 减仓排不进去（有下架清仓在跑，或拿不到组合锁）：先把组合暂停停住，下一轮再试减仓。
		if logical.AutomationState == "ACTIVE" && s.Pause != nil {
			if pauseErr := s.Pause(ctx, logical.SpaceID, logical.LogicalAccountID, reason); pauseErr != nil {
				return pauseErr
			}
			log.Warnf("交易关注项：组合 %s 保证金率过低（%s），已暂停；组合忙，减仓下一轮再做", logical.LogicalAccountID, detail)
		}
		return nil
	}
	if err != nil {
		return err
	}
	// 动作发出之后才说话：组合忙时没暂停，不能先说“已暂停并减仓”。比值只进日志，不进暂停原因和告警详情。
	log.Warnf("交易关注项：组合 %s 保证金率过低（%s），已暂停并按 %s 减仓", logical.LogicalAccountID, detail, settings.MarginReduceRatio)
	return nil
}

// collectAfterReduce 把风控减仓做完之后的归集做完：普通账户的现货卖出所得在现货钱包里，要划回合约钱包补保证金。
// 只处理由保证金风控暂停的组合的最近一次风控减仓；减仓还在跑就等，已处理过的记在进程内（重启后会再调一次，归集实现是幂等的）。
// 归集的账户有一个没就绪就先不记完成，下一轮再来。
func (s *Service) collectAfterReduce(ctx context.Context, logical store.LogicalAccountRecord, accounts []store.TradingAccountRecord) error {
	if !store.IsMarginPauseReason(logical.PauseReason) {
		return nil
	}
	action, err := s.lastMarginAction(ctx, logical)
	if err != nil || action == nil || action.Status == string(operatordomain.StatusRunning) {
		return err
	}
	s.mu.Lock()
	_, done := s.collected[action.ActionID]
	s.mu.Unlock()
	if done {
		return nil
	}
	var standard []store.TradingAccountRecord
	for _, account := range accounts {
		if inScope(account) && exchange.AccountMode(account.AccountMode) == exchange.AccountModeBinanceStandard && account.HasBalanceMarket() {
			if !account.Ready {
				return nil // 账户这一分钟没就绪：不记完成，下一轮再归集
			}
			standard = append(standard, account)
		}
	}
	if len(standard) > 0 {
		pending, err := false, ErrTransferUnavailable
		if s.Collect != nil {
			pending, err = s.Collect(ctx, logical.SpaceID, logical.LogicalAccountID, action.ActionID, standard)
		}
		switch {
		case errors.Is(err, ErrTransferUnavailable):
			s.warnOnce("collect|"+action.ActionID, fmt.Sprintf("组合 %s 触发风控后需要把现货钱包的 USDT 划回合约钱包，但划转还没有接入，请手动划转", logical.LogicalAccountID))
		case err != nil:
			return err
		case pending:
			return nil
		}
	}
	s.mu.Lock()
	if s.collected == nil {
		s.collected = make(map[string]struct{})
	}
	s.collected[action.ActionID] = struct{}{}
	s.mu.Unlock()
	return nil
}
