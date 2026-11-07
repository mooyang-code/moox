// Package marginmonitor 是交易计划 4.7 的保证金率监控：实盘账户的保证金率跌到阈值以下时，暂停组合并发起按比例减仓（REDUCE），
// 普通账户再把现货钱包的 USDT 归集回合约钱包，前后都告警，由用户确认安全后手动恢复。
//
// 口径：
//   - 币安普通账户：合约钱包权益 ÷ 合约持仓名义，不高于阈值（默认 0.5）时触发。合约钱包权益 = 合约钱包里结算币的余额（不含未实现盈亏）
//     加上永续持仓的未实现盈亏之和；持仓名义 = Σ 持仓数量绝对值 × 标记价（没有标记价用开仓均价）。
//   - 币安统一账户、OKX 统一账户：总权益 ÷ 维持保证金（币安的 uniMMR、OKX 的保证金率同一个定义），低于阈值（默认 1.5）时触发。
//     总权益取交易所给的总权益（没有则取统一公式的权益），维持保证金取账户快照里的维持保证金。
//   - 分母为零或数据缺失（没有持仓、维持保证金为零、快照太久没更新）一律不触发：数据不全时不能凭空减仓。
//   - 只看实盘生产环境、已启用且就绪的执行账户；测试网和模拟盘不监控。
package marginmonitor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

// Reduce 发起按比例减仓（先暂停组合、撤全部订单，再把现货和合约同比例减掉）。
type Reduce func(ctx context.Context, spaceID, actionID, logicalAccountID string, ratio shared.Decimal, reason string) (store.OperatorActionRecord, error)

// Collect 把普通账户现货钱包里的资金归集回合约钱包；pending 为真表示还没完成，下一轮再调（实现须幂等）。
type Collect func(ctx context.Context, spaceID, logicalAccountID, actionID string, accounts []store.TradingAccountRecord) (pending bool, err error)

// ErrTransferUnavailable 表示还没有接入钱包划转：监控只告警一次，请用户手动把现货钱包的资金划回合约钱包。
var ErrTransferUnavailable = errors.New("钱包划转还没有接入")

// marginPauseText 是风控暂停原因的开头，marginPausePrefix 带上自动暂停的前缀，用来认出“由保证金率风控暂停”的组合。
const marginPauseText = "保证金率过低"

var marginPausePrefix = store.AutoPauseReason(marginPauseText)

// ActionIDPrefix 是风控发起的减仓动作号的前缀，用来在操作记录里认出它们。
const ActionIDPrefix = "margin-"

// 默认的快照最长允许年龄：账户同步间隔是 30 秒，超过这个时间没更新说明数据不可信，不据此减仓。
const defaultMaxSnapshotAge = 3 * time.Minute

type Service struct {
	Store   *store.Store
	Reduce  Reduce
	Collect Collect
	Now     func() time.Time
	// MaxSnapshotAge 是账户快照的最长允许年龄，默认 3 分钟。
	MaxSnapshotAge time.Duration

	mu        sync.Mutex
	collected map[string]struct{}
	warned    map[string]struct{}
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
	TradingAccountID string
	// Unified 为真表示统一账户口径（总权益 ÷ 维持保证金），否则是普通账户口径（合约钱包权益 ÷ 合约持仓名义）。
	Unified   bool
	Value     shared.Decimal
	Threshold shared.Decimal
	Triggered bool
}

// Describe 是写进暂停原因和告警的说明。
func (r Reading) Describe() string {
	if r.Unified {
		return fmt.Sprintf("uniMMR %.4f 低于阈值 %s", r.Value.Float64(), r.Threshold)
	}
	return fmt.Sprintf("合约钱包权益 ÷ 持仓名义 = %.4f，不高于阈值 %s", r.Value.Float64(), r.Threshold)
}

// Applies 报告账户是否在监控范围内：实盘生产环境、已启用且就绪、开通了永续、账户类型已知。
func Applies(account store.TradingAccountRecord) bool {
	if account.ExecutionMode != string(exchange.ExecutionModeLive) || account.Environment != string(exchange.AccountEnvironmentProduction) ||
		account.Status != string(exchange.AccountStatusEnabled) || !account.Ready || !account.SupportsMarket(string(exchange.MarketTypeSwap)) {
		return false
	}
	switch exchange.AccountMode(account.AccountMode) {
	case exchange.AccountModeBinanceStandard, exchange.AccountModeBinancePortfolioMargin, exchange.AccountModeOKXUnified:
		return true
	}
	return false
}

// Evaluate 算出一个账户的保证金率读数；数据不全（分母为零、没有持仓）时 ok 为假，不触发。
func Evaluate(account store.TradingAccountRecord, positions []store.PositionRecord, settings fundsettings.Settings) (Reading, bool) {
	reading := Reading{TradingAccountID: account.TradingAccountID}
	if exchange.AccountMode(account.AccountMode) != exchange.AccountModeBinanceStandard {
		reading.Unified, reading.Threshold = true, settings.UnifiedMMRThreshold
		equity := parseOrZero(account.Snapshot.ExchangeEquity)
		if equity.Cmp(shared.Zero()) <= 0 {
			equity = parseOrZero(account.Snapshot.Equity)
		}
		maintenance := parseOrZero(account.Snapshot.MaintenanceMargin)
		if maintenance.Cmp(shared.Zero()) <= 0 {
			return reading, false
		}
		reading.Value = equity.Div(maintenance)
		reading.Triggered = reading.Value.Cmp(reading.Threshold) < 0
		return reading, true
	}
	reading.Threshold = settings.MarginRatioThreshold
	futuresWallet := string(account.WalletFor(string(exchange.MarketTypeSwap)))
	equity, notional := shared.Zero(), shared.Zero()
	for _, balance := range account.Snapshot.Balances {
		if balance.Wallet == futuresWallet && balance.Asset == account.SettlementAsset {
			equity = equity.Add(parseOrZero(balance.Total))
		}
	}
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
		equity = equity.Add(parseOrZero(position.UnrealizedPnL))
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
	if err := s.collectAfterReduce(ctx, logical); err != nil {
		log.Warnf("交易关注项：组合 %s 触发风控后的资金归集失败，下一轮重试：%v", logical.LogicalAccountID, err)
	}
	// 上一次由保证金率风控暂停、用户还没确认恢复的组合不再触发：不会每个周期都再减一次，用户手动恢复后重新监控。
	// 人工暂停、冲突自动暂停的组合照样监控：价格波动不会因为组合暂停就停下，杠杆仓位仍可能被打穿。
	if strings.HasPrefix(logical.PauseReason, marginPausePrefix) {
		return nil
	}
	running, err := s.Store.ListRunningOperatorActions(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		return nil
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return err
	}
	var triggered []Reading
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return err
		}
		if !Applies(account) {
			continue
		}
		if age := s.now().Sub(time.UnixMilli(account.LastSyncAt)); account.LastSyncAt <= 0 || age > s.maxSnapshotAge() {
			s.warnOnce("stale|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 的快照 %s 没有更新，保证金率监控这一轮跳过", account.Name, age.Round(time.Second)))
			continue
		}
		positions, err := s.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, exchange.SymbolKey{})
		if err != nil {
			return err
		}
		reading, ok := Evaluate(account, positions, settings)
		if ok && reading.Triggered {
			reading.TradingAccountID = account.Name
			triggered = append(triggered, reading)
		}
	}
	if len(triggered) == 0 {
		return nil
	}
	return s.trigger(ctx, logical, settings, triggered)
}

func (s *Service) trigger(ctx context.Context, logical store.LogicalAccountRecord, settings fundsettings.Settings, readings []Reading) error {
	detail := ""
	for _, reading := range readings {
		if detail != "" {
			detail += "；"
		}
		detail += reading.TradingAccountID + "：" + reading.Describe()
	}
	reason := store.AutoPauseReason(marginPauseText + "，已暂停并按 " + settings.MarginReduceRatio.String() + " 的比例减仓（" + detail + "）")
	// 前一次告警：触发时刻就发出，不等减仓做完。
	telemetry.Note(telemetry.AttentionMarginRatioLow, logical.Name+"："+detail, s.now())
	log.Warnf("交易关注项：组合 %s 保证金率过低（%s），暂停组合并按 %s 减仓", logical.LogicalAccountID, detail, settings.MarginReduceRatio)
	actionID := ActionIDPrefix + logical.LogicalAccountID + "-" + s.now().UTC().Format("20060102T150405")
	_, err := s.Reduce(ctx, logical.SpaceID, actionID, logical.LogicalAccountID, settings.MarginReduceRatio, reason)
	return err
}

// collectAfterReduce 把风控减仓做完之后的归集做完：普通账户的现货卖出所得在现货钱包里，要划回合约钱包补保证金。
// 只处理最近一次风控减仓；减仓还在跑就等，已处理过的记在进程内（重启后会再调一次，归集实现是幂等的）。
func (s *Service) collectAfterReduce(ctx context.Context, logical store.LogicalAccountRecord) error {
	if !store.IsAutoPauseReason(logical.PauseReason) {
		return nil
	}
	actions, _, err := s.Store.ListOperatorActions(ctx, logical.SpaceID, store.OperatorActionQuery{
		LogicalAccountID: logical.LogicalAccountID, ActionType: string(operatordomain.ActionReduce), Limit: 1,
	})
	if err != nil || len(actions) == 0 {
		return err
	}
	action := actions[0]
	if len(action.ActionID) < len(ActionIDPrefix) || action.ActionID[:len(ActionIDPrefix)] != ActionIDPrefix ||
		action.Status == string(operatordomain.StatusRunning) {
		return nil
	}
	s.mu.Lock()
	_, done := s.collected[action.ActionID]
	s.mu.Unlock()
	if done {
		return nil
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, logical.SpaceID, logical.LogicalAccountID, false)
	if err != nil {
		return err
	}
	var standard []store.TradingAccountRecord
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return err
		}
		if Applies(account) && exchange.AccountMode(account.AccountMode) == exchange.AccountModeBinanceStandard && account.HasBalanceMarket() {
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
	// 后一次告警：减仓和归集都做完了，提醒用户确认安全后手动恢复。
	telemetry.Note(telemetry.AttentionMarginRatioLow, fmt.Sprintf("%s：风控减仓已结束（%s），请确认保证金率安全后手动恢复组合", logical.Name, action.Status), s.now())
	return nil
}
