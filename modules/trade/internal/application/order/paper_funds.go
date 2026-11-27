package order

import (
	"context"
	"fmt"
	"sync"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func orderPolicyOf(validation Validation, adapter execution.ExecutionAdapter) execution.OrderPolicy {
	if validation.Policy != nil {
		return validation.Policy
	}
	return execution.ResolveOrderPolicy(adapter, validation.Account)
}

// The caller holds the account lock. Market valuation happens outside the
// SQLite transaction; cash and active reservations are read inside the write.
func (s *Service) paperMarginAdjustment(ctx context.Context, validation Validation) (shared.Decimal, error) {
	policy := orderPolicyOf(validation, nil)
	if !policy.NeedsMarginPoolSnapshot(validation.Account, validation.ReservedAsset, false) {
		return shared.Zero(), nil
	}
	if s.Adapters == nil {
		return shared.Zero(), ErrServiceConfig
	}
	adapter, err := s.Adapters.Adapter(validation.Account.ID)
	if err != nil {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "adapter", err)
	}
	policy = execution.ResolveOrderPolicy(adapter, validation.Account)
	// 账户上有挂着的单（OPEN、部分成交）时不复用：撮合随时可能让它成交，持仓和保证金会变。
	reusable, err := s.noRestingPaperOrders(ctx, validation)
	if err != nil {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "snapshot", err)
	}
	load := adapter.GetAccountSnapshot
	if !reusable {
		snapshot, loadErr := load(ctx)
		return s.marginFromSnapshot(policy, validation, snapshot, loadErr)
	}
	snapshot, err := reusedSnapshot(ctx, validation.Account.ID, load)
	return s.marginFromSnapshot(policy, validation, snapshot, err)
}

// noRestingPaperOrders 报告账户上是否没有可能被撮合的挂单（OPEN、部分成交）。
func (s *Service) noRestingPaperOrders(ctx context.Context, validation Validation) (bool, error) {
	if ctx.Value(snapshotReuseKey{}) == nil {
		return true, nil
	}
	orders, err := s.Store.ListActiveOrdersForAccount(ctx, validation.Account.SpaceID, validation.Account.ID)
	if err != nil {
		return false, err
	}
	for _, order := range orders {
		// 只看模拟撮合会成交的状态：挂单和部分成交。
		if order.State == string(orderdomain.Open) || order.State == string(orderdomain.PartiallyFilled) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Service) marginFromSnapshot(policy execution.OrderPolicy, validation Validation, snapshot exchange.AccountSnapshot, err error) (shared.Decimal, error) {
	if err != nil {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "snapshot", err)
	}
	if !snapshot.Present.UsedMargin || !snapshot.Present.UnrealizedPnL || snapshot.UsedMargin.IsNegative() ||
		snapshot.ExchangeUpdatedAt.IsZero() || snapshot.ExchangeUpdatedAt.After(s.now()) || s.now().Sub(snapshot.ExchangeUpdatedAt) > 10*time.Second {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "snapshot", fmt.Errorf("%w: paper margin valuation unavailable", ErrServiceConfig))
	}
	return policy.MarginPoolAdjustment(validation.Instrument.MarketType, snapshot.UsedMargin, snapshot.UnrealizedPnL), nil
}

// fundsMarginAdjustment 是下单资金检查用的保证金调整：减仓单在 checkLocalLedgerFunds 里不占新保证金，
// 不必为它计算账户快照。
func (s *Service) fundsMarginAdjustment(ctx context.Context, validation Validation) (shared.Decimal, error) {
	if validation.ReduceOnly {
		return shared.Zero(), nil
	}
	return s.paperMarginAdjustment(ctx, validation)
}

type snapshotReuseKey struct{}

type snapshotReuse struct {
	mu        sync.Mutex
	snapshots map[string]exchange.AccountSnapshot
}

// WithSnapshotReuse 让同一轮建子单里对同一执行账户的模拟盘保证金估值只算一次快照。
// 只用于 Converge 持逻辑账户锁的连续建单这一段：PENDING 单不参与撮合，一个执行账户只属于一个启用的组合账户，
// 这期间持仓和标记价之外的保证金因素不会变化。账户上有挂着的单（OPEN、部分成交）时不复用，每次现算。
// 发送阶段不能用：同批前面的子单可能已被撮合成交。
func WithSnapshotReuse(ctx context.Context) context.Context {
	return context.WithValue(ctx, snapshotReuseKey{}, &snapshotReuse{snapshots: make(map[string]exchange.AccountSnapshot)})
}

func reusedSnapshot(ctx context.Context, accountID string, load func(context.Context) (exchange.AccountSnapshot, error)) (exchange.AccountSnapshot, error) {
	reuse, _ := ctx.Value(snapshotReuseKey{}).(*snapshotReuse)
	if reuse == nil {
		return load(ctx)
	}
	reuse.mu.Lock()
	defer reuse.mu.Unlock()
	if snapshot, ok := reuse.snapshots[accountID]; ok {
		return snapshot, nil
	}
	snapshot, err := load(ctx)
	if err != nil {
		return snapshot, err
	}
	reuse.snapshots[accountID] = snapshot
	return snapshot, nil
}

func checkLocalLedgerFunds(tx *store.Tx, validation Validation, marginAdjustment, required shared.Decimal, oldAsset, oldQuantity string, now time.Time) error {
	if validation.ReduceOnly {
		// 减仓单不占用新保证金：已占用的保证金会在平仓时释放，只需现金覆盖手续费预留。
		// 若仍扣除已占用保证金，接近满保证金的仓位会因“可用资金不足”而无法平仓（需要先平仓才能释放保证金）。
		marginAdjustment = shared.Zero()
	}
	if oldAsset == "" && oldQuantity == "" {
		available, err := availableReservationFunds(tx, validation, marginAdjustment, now)
		if err != nil {
			return err
		}
		if available.Cmp(required) < 0 {
			return ErrInsufficientFunds
		}
		return nil
	}
	balances, err := tx.GetPaperBalanceSnapshot(validation.Account.SpaceID, validation.Account.ID)
	if err != nil {
		return err
	}
	reserved := balances.Reserved[validation.ReservedAsset]
	if oldAsset == validation.ReservedAsset && oldQuantity != "" {
		previous, err := shared.ParseDecimal(oldQuantity)
		if err != nil || previous.IsNegative() || reserved.Cmp(previous) < 0 {
			return fmt.Errorf("%w: paper replacement reservation", store.ErrInvalidRecord)
		}
		reserved = reserved.Sub(previous)
	}
	t1, err := paperT1Locked(tx, validation, now)
	if err != nil {
		return err
	}
	available := balances.Totals[validation.ReservedAsset].Add(marginAdjustment).Sub(reserved).Sub(t1)
	if available.Cmp(required) < 0 {
		return ErrInsufficientFunds
	}
	return nil
}
