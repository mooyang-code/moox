package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	logicaldomain "github.com/mooyang-code/moox/modules/trade/internal/domain/logicalaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
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
	ExchangeAccountID     string
	Enabled               bool
	Priority              int
	AdoptExistingExposure bool
}

type Readiness struct {
	Ready   bool
	Reasons []string
}

type Service struct {
	Store          *store.Store
	Syncer         AccountSyncer
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
	)
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: value.SpaceID, LogicalAccountID: value.ID, Name: value.Name,
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
	if s == nil || s.Store == nil || strings.TrimSpace(name) == "" {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
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
	if err := s.Syncer.SyncAccount(ctx, command.ExchangeAccountID); err != nil {
		return err
	}
	unlock := s.Store.LockLogicalAccount(command.SpaceID, command.LogicalAccountID)
	defer unlock()
	unlockMembership := s.Store.LockLogicalAccountMembership()
	defer unlockMembership()
	unlockExecution := s.Store.LockLogicalAccountExecution(
		command.SpaceID,
		command.LogicalAccountID,
	)
	defer unlockExecution()
	unlockAccount := s.Store.LockExchangeAccount(command.ExchangeAccountID)
	defer unlockAccount()
	exposed, err := s.memberHasExposure(
		ctx, command.SpaceID, command.ExchangeAccountID,
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
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: command.SpaceID, LogicalAccountID: command.LogicalAccountID,
			ExchangeAccountID: command.ExchangeAccountID,
			Enabled:           command.Enabled, Priority: command.Priority,
		})
	})
}

func (s *Service) RemoveMember(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	exchangeAccountID string,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	if s.Syncer == nil {
		return ErrServiceConfig
	}
	if err := s.Syncer.SyncAccount(ctx, exchangeAccountID); err != nil {
		return err
	}
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
	defer unlock()
	unlockMembership := s.Store.LockLogicalAccountMembership()
	defer unlockMembership()
	unlockExecution := s.Store.LockLogicalAccountExecution(spaceID, logicalAccountID)
	defer unlockExecution()
	unlockAccount := s.Store.LockExchangeAccount(exchangeAccountID)
	defer unlockAccount()
	exposed, err := s.memberHasExposure(ctx, spaceID, exchangeAccountID)
	if err != nil {
		return err
	}
	if exposed {
		return ErrMemberHasExposure
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember(
			spaceID, logicalAccountID, exchangeAccountID,
		)
	})
}

func (s *Service) ClaimOwner(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil ||
		strings.TrimSpace(runnerID) == "" {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
	defer unlock()
	current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	if current.OwnerRunnerID == runnerID {
		return current, nil
	}
	if current.OwnerRunnerID != "" {
		return store.LogicalAccountRecord{}, ErrOwnerConflict
	}
	accounts, err := s.Store.ListLogicalAccounts(ctx, spaceID)
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	for _, account := range accounts {
		if account.OwnerRunnerID == runnerID &&
			account.LogicalAccountID != logicalAccountID {
			return store.LogicalAccountRecord{}, ErrOwnerConflict
		}
	}
	orders, _, err := s.Store.ListOrders(ctx, spaceID, store.OrderQuery{
		LogicalAccountID: logicalAccountID,
		OnlyOpen:         true,
		Limit:            1000,
	})
	if err != nil {
		return store.LogicalAccountRecord{}, err
	}
	for _, current := range orders {
		if current.OwnerType == string(orderdomain.OwnerTarget) {
			return store.LogicalAccountRecord{}, fmt.Errorf(
				"%w: previous runner order %s is still active",
				ErrNotReady,
				current.OrderID,
			)
		}
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountOwner(
			spaceID,
			logicalAccountID,
			runnerID,
		); err != nil {
			return err
		}
		return tx.DeleteLogicalAccountTargetForOtherRunner(
			spaceID,
			logicalAccountID,
			runnerID,
		)
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.LogicalAccountRecord{}, ErrOwnerConflict
		}
		return store.LogicalAccountRecord{}, err
	}
	return s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
}

func (s *Service) ReleaseOwner(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
	defer unlock()
	current, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	if current.OwnerRunnerID == "" {
		return nil
	}
	if current.OwnerRunnerID != runnerID {
		return ErrOwnerConflict
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if current.AutomationState == "ACTIVE" {
			if err := tx.SetLogicalAccountAutomation(
				spaceID,
				logicalAccountID,
				"PAUSED",
				"runner ownership released",
			); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountOwner(spaceID, logicalAccountID, "")
	})
}

func (s *Service) Pause(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	reason string,
) (store.LogicalAccountRecord, error) {
	if s == nil || s.Store == nil || strings.TrimSpace(reason) == "" {
		return store.LogicalAccountRecord{}, ErrServiceConfig
	}
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
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
	unlock := s.Store.LockLogicalAccount(spaceID, logicalAccountID)
	defer unlock()
	before, err := s.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
	if err != nil {
		return store.LogicalAccountRecord{}, "", err
	}
	unlockExecution := s.Store.LockLogicalAccountExecution(spaceID, logicalAccountID)
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
		reasons = append(reasons, "no enabled members")
	}
	now := s.now()
	supported := make(map[string]struct{})
	for _, member := range members {
		account, getErr := s.Store.GetExchangeAccountByID(
			ctx, member.ExchangeAccountID,
		)
		if getErr != nil {
			return Readiness{}, getErr
		}
		switch {
		case account.Status != "ENABLED":
			reasons = append(reasons, account.ExchangeAccountID+" is disabled")
		case !account.Ready:
			reasons = append(reasons, account.ExchangeAccountID+" is not ready")
		case account.LastSyncAt <= 0:
			reasons = append(reasons, account.ExchangeAccountID+" has no initial sync")
		case s.snapshotStale(now, account.LastSyncAt):
			reasons = append(reasons, account.ExchangeAccountID+" snapshot is stale")
		}
		instruments, listErr := s.Store.ListInstruments(
			ctx, account.Exchange, account.MarketType,
		)
		if listErr != nil {
			return Readiness{}, listErr
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
		orders, listErr := s.Store.ListOrdersForAccount(
			ctx, spaceID, account.ExchangeAccountID, 1,
		)
		if listErr != nil {
			return Readiness{}, listErr
		}
		for _, current := range orders {
			if current.State == "SUBMITTING" || current.State == "SUBMIT_UNKNOWN" {
				reasons = append(
					reasons,
					account.ExchangeAccountID+" has unresolved submit "+current.OrderID,
				)
			}
			if current.OwnerType == "EXTERNAL" && !terminalOrderState(current.State) {
				reasons = append(
					reasons,
					account.ExchangeAccountID+" has EXTERNAL order "+current.OrderID,
				)
			}
		}
	}
	target, targetErr := s.Store.GetLogicalAccountTarget(
		ctx, spaceID, logicalAccountID,
	)
	switch {
	case targetErr == nil:
		if target.RunnerID != logicalAccount.OwnerRunnerID {
			reasons = append(reasons, "target runner does not own logical account")
		}
		for _, desired := range target.Targets {
			if _, ok := supported[desired.InstrumentID]; !ok {
				reasons = append(
					reasons,
					"target instrument "+desired.InstrumentID+" is unavailable",
				)
			}
		}
	case errors.Is(targetErr, gorm.ErrRecordNotFound):
		if logicalAccount.OwnerRunnerID != "" {
			reasons = append(reasons, "owner runner has no current target")
		}
	default:
		return Readiness{}, targetErr
	}
	sort.Strings(reasons)
	return Readiness{Ready: len(reasons) == 0, Reasons: reasons}, nil
}

func (s *Service) memberHasExposure(
	ctx context.Context,
	spaceID string,
	exchangeAccountID string,
) (bool, error) {
	account, err := s.Store.GetExchangeAccountByID(ctx, exchangeAccountID)
	if err != nil {
		return false, err
	}
	if !account.Ready || account.LastSyncAt <= 0 ||
		s.snapshotStale(s.now(), account.LastSyncAt) {
		return false, ErrNotReady
	}
	orders, err := s.Store.ListOrdersForAccount(
		ctx, spaceID, exchangeAccountID, 1,
	)
	if err != nil {
		return false, err
	}
	for _, current := range orders {
		if !terminalOrderState(current.State) {
			return true, nil
		}
	}
	positions, err := s.Store.ListPositions(ctx, spaceID, exchangeAccountID, "")
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
	if account.MarketType == string(exchange.MarketTypeSpot) {
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
