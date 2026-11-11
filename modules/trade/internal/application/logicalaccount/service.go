package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	logicaldomain "github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	ErrServiceConfig    = errors.New("组合账户服务未配置")
	ErrAdoptionRequired = errors.New("执行账户上已有持仓或挂单，加入组合需要确认接管这些已有仓位")
	// ErrMemberHasExposure 是以停用状态加入组合、或停用已有成员时，执行账户上还有挂单、持仓或受管的现货资产
	// （与移出成员同一口径：非受管资产与卖不掉的零头不算）。
	ErrMemberHasExposure = errors.New("执行账户上还有挂单、持仓或受管的现货资产，不能以停用状态加入组合，也不能停用成员，请先撤单、平仓")
	// ErrRemovalHasExposure 是移出成员时，执行账户上还有挂单、持仓或受管的现货资产（非受管资产与卖不掉的零头不算）。
	ErrRemovalHasExposure = errors.New("执行账户上还有挂单、持仓或受管的现货资产，不能移出组合，请先撤单、平仓后再移出")
	// ErrMemberNotSynced 是加入、停用或移出成员时执行账户未就绪或快照过期，持仓核对不了。
	ErrMemberNotSynced = errors.New("执行账户还没同步完成，稍后再试")
	ErrOwnerConflict   = errors.New("组合账户已被别的策略会话占用")
	ErrNotReady        = errors.New("组合账户还没准备好，暂时不能恢复自动执行")
)

type AddMemberCommand struct {
	SpaceID               string
	LogicalAccountID      string
	TradingAccountID      string
	Enabled               bool
	Priority              int
	AdoptExistingExposure bool
}

type Readiness struct {
	Ready   bool
	Reasons []string
}

type Service struct {
	Store  *store.Store
	Syncer AccountSyncer
	// Prices 用于判断现货零头的最小名义金额；为空时只比较最小数量。
	Prices         targetapp.PriceSource
	Now            func() time.Time
	MaxSnapshotAge time.Duration
	// OnFundSettingsChanged 在资金设置保存后调用，通知资金维护任务重新检查；为空时不通知。
	OnFundSettingsChanged func(spaceID, logicalAccountID string)
	// TargetOrders 在策略会话释放、重绑、认领删掉目标之前撤掉组合里还没结束的目标挂单；为空时不撤（测试用）。
	TargetOrders TargetOrderReleaser

	// releaseFailures 按“空间/组合”记释放会话撤目标单连续失败的起点和最近一次失败时间；释放撤单成功、认领或重绑撤单成功、
	// 释放请求已不作数时删掉；只在内存里，重启后重新计时。
	releaseMu       sync.Mutex
	releaseFailures map[string]releaseFailure
}

// releaseFailure 是一个组合释放会话撤目标单连续失败的计时。
type releaseFailure struct {
	since time.Time // 这一串连续失败的起点
	last  time.Time // 最近一次失败的时间
}

// releaseStuckAfter 是释放会话连续失败多久之后记关注项 release_orders_stuck。
const releaseStuckAfter = 10 * time.Minute

func releaseFailureKey(spaceID, logicalAccountID string) string {
	return spaceID + "/" + logicalAccountID
}

// releaseStuckDetail 是释放会话持续失败的关注项详情，文字固定，同一组合只占一条。
func releaseStuckDetail(logicalAccountID string) string {
	return "组合 " + logicalAccountID + "：释放会话连续失败超过 10 分钟，目标单没撤下、会话没有释放（策略在重试），" +
		"检查执行账户会话是否建得起来"
}

// releaseUnverifiedDetail 是释放会话只按本地记录放行时的关注项详情。
func releaseUnverifiedDetail(logicalAccountID string) string {
	return "组合 " + logicalAccountID + "：凭证失效、交易账户已停用或异常、标的已不存在，目标单没能撤下，只按本地记录释放会话，需人工核对"
}

// noteReleaseFailure 记下释放会话撤目标单失败：从第一次失败算起超过 releaseStuckAfter 仍在失败，就记关注项并写日志。
// 账户启用但会话一直建不起来时，释放会一直失败，不记的话没人知道这个组合的会话卡着没释放。
// 和上一次失败隔了超过 releaseStuckAfter 的不算连续失败（中间可能已经走了别的路径、没人清计时），起点重新算。
func (s *Service) noteReleaseFailure(ctx context.Context, spaceID, logicalAccountID string, cause error) {
	now := s.now()
	key := releaseFailureKey(spaceID, logicalAccountID)
	s.releaseMu.Lock()
	if s.releaseFailures == nil {
		s.releaseFailures = make(map[string]releaseFailure)
	}
	failure, found := s.releaseFailures[key]
	if !found || now.Sub(failure.last) > releaseStuckAfter {
		failure.since = now
	}
	failure.last = now
	s.releaseFailures[key] = failure
	since := failure.since
	s.releaseMu.Unlock()
	if now.Sub(since) < releaseStuckAfter {
		return
	}
	detail := releaseStuckDetail(logicalAccountID)
	telemetry.Note(telemetry.AttentionReleaseOrdersStuck, detail, now)
	log.WarnContextf(ctx, "交易关注项：%s（从 %s 起）：%v", detail, since.Format(time.RFC3339), cause)
}

// clearReleaseFailure 清掉释放会话的失败计时和关注项：释放撤目标单成功（或按本地记录放行）、认领或重绑撤目标单成功、
// 释放请求已不作数（围栏过期、会话已换）时调用。
func (s *Service) clearReleaseFailure(spaceID, logicalAccountID string) {
	key := releaseFailureKey(spaceID, logicalAccountID)
	s.releaseMu.Lock()
	_, found := s.releaseFailures[key]
	delete(s.releaseFailures, key)
	s.releaseMu.Unlock()
	if found {
		telemetry.Forget(telemetry.AttentionReleaseOrdersStuck, releaseStuckDetail(logicalAccountID))
	}
}

// TargetOrderReleaser 撤掉组合里还没结束的目标单，调用方持有组合锁（目标执行器实现）。
type TargetOrderReleaser interface {
	ReleaseTargetOrders(ctx context.Context, spaceID, logicalAccountID string) error
}

// ErrTargetOrdersNotReleased 表示释放会话前目标挂单没撤下来，会话没有释放，稍后重试。
// 它不包住撤单的原始错误（原始错误可能是版本冲突、记录不存在，会被当成“所有权冲突”“账户不存在”，策略就不再重试了），只把原因写进文案。
var ErrTargetOrdersNotReleased = errors.New("目标挂单还没撤下来，会话没有释放，稍后重试")

type targetOrdersNotReleased struct{ cause error }

func (e targetOrdersNotReleased) Error() string {
	return ErrTargetOrdersNotReleased.Error() + "：" + e.cause.Error()
}

func (e targetOrdersNotReleased) Unwrap() error { return ErrTargetOrdersNotReleased }

// releaseTargetOrders 在删掉目标之前撤掉组合里还没结束的目标单（调用方持有组合锁）。
func (s *Service) releaseTargetOrders(ctx context.Context, spaceID, logicalAccountID string) error {
	if s.TargetOrders == nil {
		return nil
	}
	if err := s.TargetOrders.ReleaseTargetOrders(ctx, spaceID, logicalAccountID); err != nil {
		return targetOrdersNotReleased{cause: err}
	}
	return nil
}

type AccountSyncer interface {
	SyncAccount(context.Context, string) error
}

func (s *Service) Create(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	name string,
	mode exchange.ExecutionMode,
	settlementAsset string,
	controlMode string,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	value, err := logicaldomain.New(
		spaceID,
		logicalAccountID,
		strings.TrimSpace(name),
		mode,
		strings.ToUpper(strings.TrimSpace(settlementAsset)),
		logicaldomain.ControlMode(controlMode),
	)
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			ControlMode: string(value.ControlMode),
			SpaceID:     value.SpaceID, LogicalAccountID: value.ID, Name: value.Name,
			ExecutionMode:   string(value.ExecutionMode),
			SettlementAsset: value.SettlementAsset,
			AutomationState: string(value.AutomationState),
			PauseReason:     value.PauseReason,
		})
	})
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	return s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
}

func (s *Service) UpdateName(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	name string,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	if strings.TrimSpace(name) == "" {
		return store.LogicalAccountRecord{}, fmt.Errorf("%w: 名称不能为空", store.ErrInvalidRecord)
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return store.LogicalAccountRecord{}, lockErr
	}
	defer unlock()
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountName(
			spaceID,
			logicalAccountID,
			strings.TrimSpace(name),
		)
	})
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	return s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
}

// UpdateOrderSettings 校验并保存下单设置。每次收敛都读最新设置，保存后对之后的收敛立即生效。
func (s *Service) UpdateOrderSettings(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	document ordersettings.Document,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	canonical, err := document.Canonical()
	if err != nil {
		return store.LogicalAccountRecord{}, fmt.Errorf("%w: %v", store.ErrInvalidRecord, err)
	}
	return s.saveSettings(ctx, spaceID, logicalAccountID, nil, func(tx *store.Tx) error {
		return tx.SetLogicalAccountOrderSettings(spaceID, logicalAccountID, canonical)
	})
}

// UpdateFundSettings 校验并保存资金设置，保存后通知资金维护任务重新检查。
func (s *Service) UpdateFundSettings(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	document fundsettings.Document,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	// 结算币在组合创建后不变，先读出来给校验用（稳定币必须含它、保证金币和底仓不能写它）。
	current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	parsed, canonical, err := fundsettings.ParseDocumentForWrite(document, current.SettlementAsset)
	if err != nil {
		return store.LogicalAccountRecord{}, fmt.Errorf("%w: %v", store.ErrInvalidRecord, err)
	}
	maintenance := parsed.BNBDiscount || parsed.BNBReserveEnabled() || parsed.DustConvert || len(parsed.StablecoinTargets) > 0
	// 下面的检查读组合和成员的当前状态，放在组合锁里做：和加成员、改成员互斥，不会检查通过后成员又变了。
	check := func(existing store.LogicalAccountRecord) error {
		// BNB 抵扣、BNB 保留、小额兑换和稳定币调平只对实盘组合生效：模拟盘组合设了也没有任何维护任务去补，
		// 换算目标时却会一直扣着 BNB 的缺口，所以直接拒绝。
		if existing.ExecutionMode == string(exchange.ExecutionModePaper) && maintenance {
			return fmt.Errorf("%w: 模拟盘组合不支持 BNB 抵扣、BNB 保留、小额兑换和稳定币保证金（只对实盘生效）", store.ErrInvalidRecord)
		}
		if !maintenance {
			return nil
		}
		return s.requireBinanceMembers(ctx, spaceID, logicalAccountID)
	}
	record, err := s.saveSettings(ctx, spaceID, logicalAccountID, check, func(tx *store.Tx) error {
		return tx.SetLogicalAccountFundSettings(spaceID, logicalAccountID, canonical)
	})
	if err == nil && s.OnFundSettingsChanged != nil {
		s.OnFundSettingsChanged(spaceID, logicalAccountID)
	}
	return record, err
}

// requireBinanceMembers 要求组合的全部成员（含停用成员）都是币安账户：BNB 抵扣、BNB 保留、小额兑 BNB、稳定币调平都是币安的功能。
func (s *Service) requireBinanceMembers(ctx context.Context, spaceID, logicalAccountID string) error {
	members, err := s.Store.ListLogicalAccountMembers(ctx, spaceID, logicalAccountID, true)
	if err != nil {
		return err
	}
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return err
		}
		if account.Exchange != string(exchange.ExchangeBinance) {
			return fmt.Errorf("%w: 成员 %s 不是币安账户，BNB 抵扣、BNB 保留、小额兑换和稳定币保证金只适用于币安", store.ErrInvalidRecord, accountLabel(account))
		}
	}
	return nil
}

// checkMemberAgainstFundSettings 加成员（或改成员）时复查资金设置：组合已经开了 BNB 项或稳定币调平，不能再加非币安成员，
// 否则先开设置再加成员就能绕过写设置时的检查。调用方已经持有组合锁。
func (s *Service) checkMemberAgainstFundSettings(ctx context.Context, command AddMemberCommand) error {
	logical, err := s.Store.GetLogicalAccount(ctx, command.SpaceID, command.LogicalAccountID)
	if err != nil {
		return err
	}
	settings, err := s.Store.ResolveFundSettings(logical)
	if err != nil {
		return nil // 设置读不出来时执行器也不下单，这里不再叠加拒绝
	}
	if !settings.BNBDiscount && !settings.BNBReserveEnabled() && !settings.DustConvert && len(settings.StablecoinTargets) == 0 {
		return nil
	}
	account, err := s.Store.GetTradingAccountByID(ctx, command.TradingAccountID)
	if err != nil {
		return err
	}
	if account.Exchange != string(exchange.ExchangeBinance) {
		return fmt.Errorf("%w: 组合开了 BNB 抵扣、BNB 保留、小额兑换或稳定币保证金，只能加币安账户，%s 不是", store.ErrInvalidRecord, accountLabel(account))
	}
	return nil
}

func (s *Service) saveSettings(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	check func(store.LogicalAccountRecord) error,
	write func(*store.Tx) error,
) (store.LogicalAccountRecord, error) {
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return store.LogicalAccountRecord{}, lockErr
	}
	defer unlock()
	if check != nil {
		existing, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
		if err != nil {
			return store.LogicalAccountRecord{}, err
		}
		if err := check(existing); err != nil {
			return store.LogicalAccountRecord{}, err
		}
	}
	if err := s.Store.Transaction(ctx, write); err != nil {
		return store.LogicalAccountRecord{}, err
	}
	return s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
}

// AddMember 加入成员或修改成员（启停、优先级），返回给用户的提示（正常时为空）。
func (s *Service) AddMember(ctx context.Context, command AddMemberCommand) (string, error) {
	if s == nil || s.Store == nil {
		return "", ErrServiceConfig
	}
	if s.Syncer == nil {
		return "", ErrServiceConfig
	}
	// 归属检查放在同步和持仓检查之前，否则已属于别的组合的账户会先看到无关的错误。
	if owner, _, err := s.Store.FindLogicalAccountMembership(ctx, command.SpaceID, command.TradingAccountID); err == nil {
		if owner.LogicalAccountID != command.LogicalAccountID {
			return "", fmt.Errorf("%w（组合账户 %s）", logicaldomain.ErrMemberElsewhere, owner.Name)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, command.SpaceID, command.LogicalAccountID, true)
	if err != nil {
		return "", err
	}
	for _, member := range members {
		// 启用成员只改优先级时不碰持仓，有持仓的成员也能调整。
		if member.TradingAccountID == command.TradingAccountID && member.Enabled && command.Enabled {
			unlockAll, lockErr := s.Store.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, command.TradingAccountID)
			if lockErr != nil {
				return "", lockErr
			}
			defer unlockAll()
			return "", s.putMember(ctx, command)
		}
	}
	if !command.Enabled {
		return s.putDisabledMember(ctx, command)
	}
	if err := s.Syncer.SyncAccount(ctx, command.TradingAccountID); err != nil {
		return "", err
	}
	// 全部带期限：任何一把锁等待超时都释放已取得的锁并返回，不占住全局成员锁
	unlockAll, lockErr := s.Store.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, command.TradingAccountID)
	if lockErr != nil {
		return "", lockErr
	}
	defer unlockAll()
	exposed, err := s.memberHasExposure(
		ctx, command.SpaceID, command.TradingAccountID,
	)
	if err != nil {
		return "", err
	}
	if exposed && !command.AdoptExistingExposure {
		return "", ErrAdoptionRequired
	}
	return "", s.putMember(ctx, command)
}

// putDisabledMember 以停用状态加入组合，或停用已有成员。停用后组合不再管这个账户，所以和移出成员同一口径：
// 只看挂单、持仓和受管资产（非受管资产与卖不掉的零头不算）；凭证失效或没有会话时只按本地记录确认，返回提示。
func (s *Service) putDisabledMember(ctx context.Context, command AddMemberCommand) (string, error) {
	syncErr := s.Syncer.SyncAccount(ctx, command.TradingAccountID)
	// 只有凭证失效或账户没有会话（同步永远不会成功）才退到只看本地记录；超时、限频等暂时性失败照旧报错。
	if syncErr != nil && (ctx.Err() != nil || !credentialUnavailable(syncErr)) {
		return "", syncErr
	}
	unlockAll, lockErr := s.Store.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, command.TradingAccountID)
	if lockErr != nil {
		return "", lockErr
	}
	defer unlockAll()
	exposed, err := s.managedExposure(ctx, command.SpaceID, command.TradingAccountID, syncErr == nil)
	if err != nil {
		return "", errors.Join(syncErr, err)
	}
	if exposed {
		if syncErr != nil {
			return "", fmt.Errorf("%w（同步失败：%s）", ErrMemberHasExposure, syncFailureSummary(syncErr))
		}
		return "", ErrMemberHasExposure
	}
	if err := s.putMember(ctx, command); err != nil {
		return "", err
	}
	if syncErr != nil {
		return UnverifiedMemberWarning, nil
	}
	return "", nil
}

func (s *Service) putMember(ctx context.Context, command AddMemberCommand) error {
	if err := s.checkMemberAgainstFundSettings(ctx, command); err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: command.SpaceID, LogicalAccountID: command.LogicalAccountID,
			TradingAccountID: command.TradingAccountID,
			Enabled:          command.Enabled, Priority: command.Priority,
		})
	})
}

// credentialUnavailable 判断同步失败是不是因为凭证失效或账户没有会话。
func credentialUnavailable(err error) bool {
	if exchange.IsKind(err, exchange.ErrorAuthentication) {
		return true
	}
	var accountErr *orderapp.AccountExecutionError
	return errors.As(err, &accountErr) && accountErr.Operation == "adapter"
}

// allOrdersUnreachable 判断撤目标单失败是否每一支都是等多久都不会成功的：凭证失效、交易所确认标的不存在、
// 取不到账户会话而交易账户已经停用或处于异常（ERROR）状态。取不到会话但交易账户仍启用（例如重启后会话还没建好）只是暂时的，不算；
// 查不到交易账户状态也不算。多笔单的错误是 errors.Join 起来的，只要有一支是别的错误（例如超时、限流），就不算，照样让调用方重试。
func (s *Service) allOrdersUnreachable(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		for _, child := range children {
			if !s.allOrdersUnreachable(ctx, child) {
				return false
			}
		}
		return len(children) > 0
	}
	if exchange.IsKind(err, exchange.ErrorAuthentication) || exchange.IsInstrumentNotFound(err) {
		return true
	}
	var accountErr *orderapp.AccountExecutionError
	if !errors.As(err, &accountErr) || accountErr.Operation != "adapter" {
		return false
	}
	account, getErr := s.Store.GetTradingAccountByID(ctx, accountErr.TradingAccountID)
	return getErr == nil && (account.Status == string(exchange.AccountStatusDisabled) ||
		account.Status == string(exchange.AccountStatusError))
}

// syncFailureSummary 是给前端看的同步失败原因：用中文说清是没有会话还是凭证失效，不带交易所原文（可能是英文或带请求串）。
func syncFailureSummary(err error) string {
	var accountErr *orderapp.AccountExecutionError
	switch {
	case errors.As(err, &accountErr) && accountErr.Operation == "adapter":
		return "账户会话没有启动或账户已停用"
	case exchange.IsKind(err, exchange.ErrorAuthentication):
		return "凭证失效，" + exchange.FailureSummary(err)
	}
	return exchange.FailureSummary(err)
}

// UnverifiedMemberWarning 是同步失败、只凭本地记录移除或停用成员时返回的提示。
const UnverifiedMemberWarning = "执行账户同步失败（例如凭证已失效），只按本地记录确认没有挂单和持仓，交易所侧仓位没有核对"

// RemoveMember 移除成员，返回给用户的提示（正常移除时为空）。凭证失效等原因同步失败时，
// 本地确认没有未结束订单、持仓和受管余额也允许移除，否则账户永远移不出组合。
func (s *Service) RemoveMember(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	tradingAccountID string,
) (string, error) {
	if s == nil || s.Store == nil {
		return "", ErrServiceConfig
	}
	if s.Syncer == nil {
		return "", ErrServiceConfig
	}
	syncErr := s.Syncer.SyncAccount(ctx, tradingAccountID)
	// 只有凭证失效或账户没有会话（同步永远不会成功）才退到只看本地记录；超时、限频等暂时性失败照旧报错。
	if syncErr != nil && (ctx.Err() != nil || !credentialUnavailable(syncErr)) {
		return "", syncErr
	}
	unlockAll, lockErr := s.Store.LockMemberChange(ctx, spaceID, logicalAccountID, tradingAccountID)
	if lockErr != nil {
		return "", lockErr
	}
	defer unlockAll()
	exposed, err := s.managedExposure(ctx, spaceID, tradingAccountID, syncErr == nil)
	if err != nil {
		return "", errors.Join(syncErr, err)
	}
	if exposed {
		if syncErr != nil {
			// 本地还有挂单、持仓或受管资产：照样按“有持仓不能移出”报，不能只返回同步错误（没有会话时会被当成“稍后重试”，
			// 重试多少次都不会成功，也看不出是有持仓）。同步错误只作为原因附上，不参与错误码判断。
			return "", fmt.Errorf("%w（同步失败：%s）", ErrRemovalHasExposure, syncFailureSummary(syncErr))
		}
		return "", ErrRemovalHasExposure
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember(
			spaceID, logicalAccountID, tradingAccountID,
		)
	}); err != nil {
		return "", err
	}
	if syncErr != nil {
		return UnverifiedMemberWarning, nil
	}
	return "", nil
}

// ClaimSession assigns the explicit Strategy instance/session identity used by
// target events, with auth_fence compare-and-set semantics.
func (s *Service) ClaimSession(
	ctx context.Context,
	spaceID, logicalAccountID, instanceID, sessionID, expectedFence string,
) (store.LogicalAccountRecord, string, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, "", ErrServiceConfig
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return store.LogicalAccountRecord{}, "", lockErr
	}
	defer unlock()
	// 认领会删掉目标：先撤掉上一个会话可能留下的挂单。撤不下来也照常认领，新会话的第一份目标在第 1 阶段会再撤（executor.go）。
	if current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID); err == nil && claimChanges(current, instanceID, sessionID, expectedFence) {
		if err := s.releaseTargetOrders(ctx, spaceID, logicalAccountID); err != nil {
			log.WarnContextf(ctx, "交易关注项：组合 %s 认领会话前撤目标挂单失败，交给新目标的第 1 阶段处理：%v", logicalAccountID, err)
		} else {
			s.clearReleaseFailure(spaceID, logicalAccountID)
		}
	}
	var fence string
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var err error
		var changed bool
		fence, changed, err = tx.ClaimLogicalAccountSession(spaceID, logicalAccountID, instanceID, sessionID, expectedFence)
		if err != nil || !changed {
			return err
		}
		return tx.DeleteLogicalAccountTarget(spaceID, logicalAccountID)
	})
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	value, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	return value, fence, err
}

// ReleaseSession is a compare-and-set release. A stale session cannot clear
// a replacement session's authorization or target.
func (s *Service) ReleaseSession(
	ctx context.Context,
	spaceID, logicalAccountID, instanceID, sessionID, expectedFence string,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	// 释放会删掉目标，之后没有人再管这个组合的目标单。顺序：先删目标（执行器不会再按它下新单），再撤目标单，最后做所有权 CAS。
	// 撤不下来就不释放、返回错误，由策略稍后重试释放（目标已删，重试时只撤单）。撤单按组合查单，不依赖目标。
	// 凭证失效、交易账户已停用或异常（没有会话）时撤单永远不会成功：比照移出成员，只按本地记录放行，记关注项，交给人工核对；
	// 交易所确认标的不存在（已下架）的单也不可能还挂着，同样放行。账户仍启用只是暂时没有会话（例如重启后还没建好）时不放行，让策略重试；
	// 连续失败超过 10 分钟记关注项 release_orders_stuck（见 noteReleaseFailure）。
	current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if releaseChanges(current, instanceID, sessionID, expectedFence) {
		if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
			return tx.DeleteLogicalAccountTarget(spaceID, logicalAccountID)
		}); err != nil {
			return err
		}
		if s.TargetOrders != nil {
			if err := s.TargetOrders.ReleaseTargetOrders(ctx, spaceID, logicalAccountID); err != nil {
				if ctx.Err() != nil || !s.allOrdersUnreachable(ctx, err) {
					s.noteReleaseFailure(ctx, spaceID, logicalAccountID, err)
					return targetOrdersNotReleased{cause: err}
				}
				detail := releaseUnverifiedDetail(logicalAccountID)
				telemetry.Note(telemetry.AttentionReleaseOrdersUnverified, detail, s.now())
				log.WarnContextf(ctx, "交易关注项：%s：%v", detail, err)
			}
		}
	}
	// 撤单成功、按本地记录放行，或者这次释放不作数（围栏过期、会话已换，之前没释放下来的那个会话已经不在了）：清掉失败计时。
	s.clearReleaseFailure(spaceID, logicalAccountID)
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.ReleaseLogicalAccountSession(spaceID, logicalAccountID, instanceID, sessionID, expectedFence); err != nil {
			return err
		}
		return tx.DeleteLogicalAccountTarget(spaceID, logicalAccountID)
	})
}

// claimChanges、releaseChanges 按 store 里会话 CAS 的同样条件预判这次操作会不会换掉所有者、删掉目标：
// 只有会删目标时才撤目标挂单。幂等的重复请求、过期的围栏、被别的会话占用时都不撤，否则会撤掉当前所有者正在用的挂单。
// 调用方持有组合锁，会话字段只在这把锁下改，预判和随后的 CAS 之间不会变。
func claimChanges(account store.LogicalAccountRecord, instanceID, sessionID, expectedFence string) bool {
	// 认领只在组合空闲时生效（同一会话重复认领时所有者非空，不会走到这里）。
	return account.ControlMode == "STRATEGY" && account.AuthFence == expectedFence &&
		account.OwnerInstanceID == "" && account.OwnerSessionID == "" && instanceID != "" && sessionID != ""
}

// releaseChanges 对已经空闲的组合也返回真：释放照样删目标，重试释放时要接着撤上一次没撤下来的挂单。
func releaseChanges(account store.LogicalAccountRecord, instanceID, sessionID, expectedFence string) bool {
	if account.AuthFence != expectedFence {
		return false
	}
	idle := account.OwnerInstanceID == "" && account.OwnerSessionID == ""
	return idle || account.OwnerInstanceID == instanceID && account.OwnerSessionID == sessionID
}

func (s *Service) Pause(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	reason string,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	if strings.TrimSpace(reason) == "" {
		return store.LogicalAccountRecord{}, fmt.Errorf("%w: 暂停原因不能为空", store.ErrInvalidRecord)
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return store.LogicalAccountRecord{}, lockErr
	}
	defer unlock()
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			spaceID, logicalAccountID, "PAUSED", strings.TrimSpace(reason),
		)
	}); err != nil {
		return store.LogicalAccountRecord{}, err
	}
	return s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
}

// PauseIf 只在组合当前的状态和暂停原因仍是预期值时暂停（改写原因），返回是否改了。
func (s *Service) PauseIf(ctx context.Context, spaceID, logicalAccountID, expectedState, expectedReason, reason string) (bool, error) {
	if s == nil || s.Store == nil {
		return false, ErrServiceConfig
	}
	if strings.TrimSpace(reason) == "" {
		return false, fmt.Errorf("%w: 暂停原因不能为空", store.ErrInvalidRecord)
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return false, lockErr
	}
	defer unlock()
	var paused bool
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var pauseErr error
		paused, pauseErr = tx.PauseLogicalAccountIf(spaceID, logicalAccountID, expectedState, expectedReason, strings.TrimSpace(reason))
		return pauseErr
	})
	return paused, err
}

func (s *Service) Resume(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (store.LogicalAccountRecord, string, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, "", ErrServiceConfig
	}
	unlock, err := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	defer unlock()
	before, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	if before.ControlMode != "STRATEGY" {
		return store.LogicalAccountRecord{}, "", ErrNotReady
	}
	unlockExecution, err := s.Store.LockLogicalAccountExecutionContext(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	defer unlockExecution()
	current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	if current.AutomationState != before.AutomationState ||
		current.PauseReason != before.PauseReason {
		return store.LogicalAccountRecord{}, "", fmt.Errorf(
			"%w：恢复期间组合的状态被改动了，请重试",
			ErrNotReady,
		)
	}
	readiness, err := s.readiness(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	if !readiness.Ready {
		return store.LogicalAccountRecord{}, "", fmt.Errorf(
			"%w：%s", ErrNotReady, strings.Join(readiness.Reasons, "；"),
		)
	}
	running, err := s.Store.ListRunningOperatorActions(
		ctx, spaceID, logicalAccountID,
	)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	if len(running) > 0 {
		return store.LogicalAccountRecord{}, "",
			fmt.Errorf("%w：还有人工操作在执行", ErrNotReady)
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			spaceID, logicalAccountID, "ACTIVE", "",
		)
	}); err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	current, err = s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	return current, "恢复后会按最新目标重新收敛，人工清仓后的仓位可能重新开仓", err
}

func (s *Service) Readiness(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (Readiness, error) {
	if s == nil || s.Store == nil {
		return Readiness{}, ErrServiceConfig
	}
	return s.readiness(ctx, spaceID, logicalAccountID)
}

func (s *Service) readiness(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) (Readiness, error) {
	logicalAccount, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return Readiness{}, err
	}
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, spaceID, logicalAccountID, false,
	)
	if err != nil {
		return Readiness{}, err
	}
	reasons := make([]string, 0)
	if len(members) == 0 {
		reasons = append(reasons, "没有启用的成员")
	}
	now := s.now()
	for _, member := range members {
		account, getErr := s.Store.GetTradingAccountByID(
			ctx, member.TradingAccountID,
		)
		if getErr != nil {
			return Readiness{}, getErr
		}
		// 原因会显示在页面上、拼进恢复被拒的报错，用账户名称。
		label := accountLabel(account)
		switch {
		case account.Status != "ENABLED":
			reasons = append(reasons, label+" 已停用")
		case !account.Ready:
			reasons = append(reasons, label+" 未就绪")
		case account.LastSyncAt <= 0:
			reasons = append(reasons, label+" 尚未完成首次同步")
		case s.snapshotStale(now, account.LastSyncAt):
			reasons = append(reasons, label+" 账户快照已过期")
		}
		orders, listErr := s.Store.ListActiveOrdersForAccount(ctx, spaceID, account.TradingAccountID)
		if listErr != nil {
			return Readiness{}, listErr
		}
		for _, current := range orders {
			if current.State == "SUBMITTING" || current.State == "SUBMIT_UNKNOWN" {
				reasons = append(
					reasons,
					label+" 有未确认的提交订单 "+current.OrderID,
				)
			}
			if current.OwnerType == "EXTERNAL" {
				reasons = append(
					reasons,
					label+" 有外部挂单 "+current.OrderID+"（不是 MooX 下的单），撤掉后再恢复",
				)
			}
		}
	}
	if logicalAccount.ControlMode == "MANUAL" {
		sort.Strings(reasons)
		return Readiness{Ready: len(reasons) == 0, Reasons: reasons}, nil
	}
	target, targetErr := s.Store.GetLogicalAccountTarget(
		ctx, spaceID, logicalAccountID,
	)
	switch {
	case targetErr == nil:
		if target.InstanceID == "" || target.SessionID == "" ||
			target.InstanceID != logicalAccount.OwnerInstanceID || target.SessionID != logicalAccount.OwnerSessionID {
			reasons = append(reasons, "目标所属会话不再持有组合账户")
		}
		// Resume enables automation, not an expired target. Execution waits
		// for the next valid target and checks its validity window itself.
		if now.UnixMilli() >= target.ValidUntil {
			break
		}
		supported, supportErr := s.supportedInstruments(ctx, members)
		if supportErr != nil {
			return Readiness{}, supportErr
		}
		for _, desired := range target.Targets {
			// 保持标的不会被下单，不因它暂时不可用而阻止恢复自动化。
			if desired.Hold {
				continue
			}
			if _, ok := supported[desired.InstrumentID]; !ok {
				reasons = append(
					reasons,
					"目标里的标的 "+desired.InstrumentID+" 在启用的执行账户上都不能交易（没有这个标的、已暂停或下架，或结算资产不同）",
				)
			}
		}
	case errors.Is(targetErr, gorm.ErrRecordNotFound):
	default:
		return Readiness{}, targetErr
	}
	sort.Strings(reasons)
	return Readiness{Ready: len(reasons) == 0, Reasons: reasons}, nil
}

// accountLabel 是给用户看的执行账户名称，没有名称时退回编号。
func accountLabel(account store.TradingAccountRecord) string {
	if account.Name != "" {
		return account.Name
	}
	return account.TradingAccountID
}

func (s *Service) supportedInstruments(ctx context.Context, members []store.LogicalAccountMemberRecord) (map[string]struct{}, error) {
	supported := make(map[string]struct{})
	for _, member := range members {
		account, err := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if err != nil {
			return nil, err
		}
		instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return nil, err
		}
		for _, instrument := range instruments {
			if !exchange.StatusTradable(instrument.Status) {
				continue
			}
			if instrument.SettlementAsset != "" &&
				instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			supported[instrument.InstrumentID] = struct{}{}
		}
	}
	return supported, nil
}

func (s *Service) memberHasExposure(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) (bool, error) {
	exposed, _, err := s.exposure(ctx, spaceID, tradingAccountID, true)
	return exposed, err
}

// exposureFacts 是 exposure 查到的账户、挂单和持仓，managedExposure 直接复用，不再重复查询。
// 有挂单时 exposure 提前返回，positions 为空。
type exposureFacts struct {
	account   store.TradingAccountRecord
	orders    []store.OrderRecord
	positions []store.PositionRecord
}

// exposure 判断执行账户是否有挂单、持仓或非结算资产余额；requireFresh 为真时要求账户就绪且快照新鲜，
// 否则返回 ErrMemberNotSynced（只有成员变更用它）。
func (s *Service) exposure(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	requireFresh bool,
) (bool, exposureFacts, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return false, exposureFacts{}, err
	}
	facts := exposureFacts{account: account}
	if requireFresh && (!account.Ready || account.LastSyncAt <= 0 ||
		s.snapshotStale(s.now(), account.LastSyncAt)) {
		return false, facts, ErrMemberNotSynced
	}
	facts.orders, err = s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, facts, err
	}
	if len(facts.orders) > 0 {
		return true, facts, nil
	}
	facts.positions, err = s.Store.ListPositions(ctx, spaceID, tradingAccountID, exchange.SymbolKey{})
	if err != nil {
		return false, facts, err
	}
	for _, position := range facts.positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			return false, facts, parseErr
		}
		if !quantity.IsZero() {
			return true, facts, nil
		}
	}
	if account.HasBalanceMarket() {
		for _, balance := range account.SpotBalances() {
			if balance.Asset == account.SettlementAsset {
				continue
			}
			total, parseErr := shared.ParseDecimal(balance.Total)
			if parseErr != nil {
				return false, facts, parseErr
			}
			if !total.IsZero() {
				return true, facts, nil
			}
		}
	}
	return false, facts, nil
}

// managedExposure 用于移除和停用成员：非受管资产不归 MooX 管，不挡移除和停用。
func (s *Service) managedExposure(ctx context.Context, spaceID, tradingAccountID string, requireFresh bool) (bool, error) {
	exposed, facts, err := s.exposure(ctx, spaceID, tradingAccountID, requireFresh)
	if err != nil || !exposed {
		return false, err
	}
	account := facts.account
	// 有挂单：下面无论哪条路径都判有敞口，直接返回。
	if len(facts.orders) > 0 {
		return true, nil
	}
	if account.HasPositionMarket() {
		for _, position := range facts.positions {
			quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
			if parseErr != nil {
				return false, parseErr
			}
			if !quantity.IsZero() {
				return true, nil
			}
		}
	}
	if !account.HasBalanceMarket() {
		return true, nil
	}
	managed, err := s.Store.ManagedAssets(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, err
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, tradingAccountID)
	if err != nil {
		return false, err
	}
	// 与清仓同一套规则：按总量判断，卖出后留下的零头下不了单，不挡移除。
	byAsset := exposure.SpotInstruments(instruments, account.SettlementAsset)
	for _, balance := range account.SpotBalances() {
		total, parseErr := shared.ParseDecimal(balance.Total)
		if parseErr != nil {
			return false, parseErr
		}
		if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset || total.IsZero() {
			continue
		}
		instrument, found := byAsset[balance.Asset]
		if !found {
			return true, nil
		}
		price := shared.Zero()
		if s.Prices != nil {
			if quote, quoteErr := s.Prices.LatestPrice(ctx, tradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)); quoteErr == nil {
				price = quote.Price
			}
		}
		if !exposure.SpotDust(total, price, instrument) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) snapshotStale(now time.Time, lastSyncAt int64) bool {
	maxAge := s.MaxSnapshotAge
	if maxAge <= 0 {
		maxAge = time.Minute
	}
	return now.Sub(time.UnixMilli(lastSyncAt)) > maxAge
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
