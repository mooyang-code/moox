package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	logicaldomain "github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var (
	ErrServiceConfig     = errors.New("trade logical account: service is not configured")
	ErrAdoptionRequired  = errors.New("trade logical account: adoption is required")
	ErrMemberHasExposure = errors.New("trade logical account: member has active exposure")
	ErrOwnerConflict     = errors.New("trade logical account: runner ownership conflict")
	ErrNotReady          = errors.New("trade logical account: not ready")
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
	market exchange.MarketType,
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
		market,
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
			MarketType:      string(value.MarketType),
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

func (s *Service) AddMember(ctx context.Context, command AddMemberCommand) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	if s.Syncer == nil {
		return ErrServiceConfig
	}
	// 归属检查放在同步和持仓检查之前，否则已属于别的组合的账户会先看到无关的错误。
	if owner, _, err := s.Store.FindLogicalAccountMembership(ctx, command.SpaceID, command.TradingAccountID); err == nil {
		if owner.LogicalAccountID != command.LogicalAccountID {
			return fmt.Errorf("%w（组合账户 %s）", logicaldomain.ErrMemberElsewhere, owner.Name)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	members, err := s.Store.ListLogicalAccountMembers(ctx, command.SpaceID, command.LogicalAccountID, true)
	if err != nil {
		return err
	}
	for _, member := range members {
		// 启用成员只改优先级时不碰持仓，有持仓的成员也能调整。
		if member.TradingAccountID == command.TradingAccountID && member.Enabled && command.Enabled {
			unlockAll, lockErr := s.Store.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, command.TradingAccountID)
			if lockErr != nil {
				return lockErr
			}
			defer unlockAll()
			return s.putMember(ctx, command)
		}
	}
	if err := s.Syncer.SyncAccount(ctx, command.TradingAccountID); err != nil {
		return err
	}
	// 全部带期限：任何一把锁等待超时都释放已取得的锁并返回，不占住全局成员锁
	unlockAll, lockErr := s.Store.LockMemberChange(ctx, command.SpaceID, command.LogicalAccountID, command.TradingAccountID)
	if lockErr != nil {
		return lockErr
	}
	defer unlockAll()
	exposed, err := s.memberHasExposure(
		ctx, command.SpaceID, command.TradingAccountID,
	)
	if err != nil {
		return err
	}
	if exposed {
		if !command.Enabled {
			return ErrMemberHasExposure
		}
		if !command.AdoptExistingExposure {
			return ErrAdoptionRequired
		}
	}
	return s.putMember(ctx, command)
}

func (s *Service) putMember(ctx context.Context, command AddMemberCommand) error {
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: command.SpaceID, LogicalAccountID: command.LogicalAccountID,
			TradingAccountID: command.TradingAccountID,
			Enabled:          command.Enabled, Priority: command.Priority,
		})
	})
}

// UnverifiedRemovalWarning 是同步失败、只凭本地记录移除成员时返回的提示。
const UnverifiedRemovalWarning = "执行账户同步失败（例如凭证已失效），只按本地记录确认没有挂单和持仓，交易所侧仓位没有核对"

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
	if syncErr != nil && ctx.Err() != nil {
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
			return "", syncErr
		}
		return "", ErrMemberHasExposure
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember(
			spaceID, logicalAccountID, tradingAccountID,
		)
	}); err != nil {
		return "", err
	}
	if syncErr != nil {
		return UnverifiedRemovalWarning, nil
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

// RebindSession atomically switches an existing Strategy session. The old
// target is cleared only when this CAS wins, so a delayed rebind cannot erase
// a target accepted by a newer session.
func (s *Service) RebindSession(
	ctx context.Context,
	spaceID, logicalAccountID, oldInstanceID, oldSessionID, expectedFence,
	newInstanceID, newSessionID string,
) (store.LogicalAccountRecord, string, error) {
	if s == nil || s.Store == nil {
		return store.LogicalAccountRecord{}, "", ErrServiceConfig
	}
	unlock, lockErr := s.Store.LockLogicalAccountContext(ctx, spaceID, logicalAccountID)
	if lockErr != nil {
		return store.LogicalAccountRecord{}, "", lockErr
	}
	defer unlock()
	var fence string
	err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var err error
		var changed bool
		fence, changed, err = tx.RebindLogicalAccountSession(spaceID, logicalAccountID, oldInstanceID, oldSessionID, expectedFence, newInstanceID, newSessionID)
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
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.ReleaseLogicalAccountSession(spaceID, logicalAccountID, instanceID, sessionID, expectedFence); err != nil {
			return err
		}
		return tx.DeleteLogicalAccountTarget(spaceID, logicalAccountID)
	})
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
			"%w: account facts changed while resuming",
			ErrNotReady,
		)
	}
	readiness, err := s.readiness(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	if !readiness.Ready {
		return store.LogicalAccountRecord{}, "", fmt.Errorf(
			"%w: %s", ErrNotReady, strings.Join(readiness.Reasons, "; "),
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
			fmt.Errorf("%w: operator action is still running", ErrNotReady)
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
		switch {
		case account.Status != "ENABLED":
			reasons = append(reasons, account.TradingAccountID+" 已停用")
		case !account.Ready:
			reasons = append(reasons, account.TradingAccountID+" 未就绪")
		case account.LastSyncAt <= 0:
			reasons = append(reasons, account.TradingAccountID+" 尚未完成首次同步")
		case s.snapshotStale(now, account.LastSyncAt):
			reasons = append(reasons, account.TradingAccountID+" 账户快照已过期")
		}
		orders, listErr := s.Store.ListActiveOrdersForAccount(ctx, spaceID, account.TradingAccountID)
		if listErr != nil {
			return Readiness{}, listErr
		}
		for _, current := range orders {
			if current.State == "SUBMITTING" || current.State == "SUBMIT_UNKNOWN" {
				reasons = append(
					reasons,
					account.TradingAccountID+" 有未确认的提交订单 "+current.OrderID,
				)
			}
			if current.OwnerType == "EXTERNAL" && !terminalOrderState(current.State) {
				reasons = append(
					reasons,
					account.TradingAccountID+" 有外部订单 EXTERNAL "+current.OrderID,
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
					"target instrument "+desired.InstrumentID+" is unavailable",
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
			if instrument.Status != "TRADING" && instrument.Status != "live" {
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
	return s.exposure(ctx, spaceID, tradingAccountID, true)
}

// exposure 判断执行账户是否有挂单、持仓或非结算资产余额；requireFresh 为真时要求账户就绪且快照新鲜。
func (s *Service) exposure(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	requireFresh bool,
) (bool, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return false, err
	}
	if requireFresh && (!account.Ready || account.LastSyncAt <= 0 ||
		s.snapshotStale(s.now(), account.LastSyncAt)) {
		return false, ErrNotReady
	}
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, err
	}
	for _, current := range orders {
		if !terminalOrderState(current.State) {
			return true, nil
		}
	}
	positions, err := s.Store.ListPositions(ctx, spaceID, tradingAccountID, "")
	if err != nil {
		return false, err
	}
	for _, position := range positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			return false, parseErr
		}
		if !quantity.IsZero() {
			return true, nil
		}
	}
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
		for _, balance := range account.Snapshot.Balances {
			if balance.Asset == account.SettlementAsset {
				continue
			}
			total, parseErr := shared.ParseDecimal(balance.Total)
			if parseErr != nil {
				return false, parseErr
			}
			if !total.IsZero() {
				return true, nil
			}
		}
	}
	return false, nil
}

// managedExposure 用于移除成员：非受管资产不归 MooX 管，不挡移除。
func (s *Service) managedExposure(ctx context.Context, spaceID, tradingAccountID string, requireFresh bool) (bool, error) {
	exposed, err := s.exposure(ctx, spaceID, tradingAccountID, requireFresh)
	if err != nil || !exposed {
		return false, err
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return false, err
	}
	if !exchange.RuleFor(account.MarketType).PositionFromBalance {
		return true, nil
	}
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return false, err
	}
	if len(orders) > 0 {
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
	for _, balance := range account.Snapshot.Balances {
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
			if quote, quoteErr := s.Prices.LatestPrice(ctx, tradingAccountID, instrument.ExchangeSymbol); quoteErr == nil {
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

func terminalOrderState(state string) bool {
	switch state {
	case "FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED":
		return true
	default:
		return false
	}
}
