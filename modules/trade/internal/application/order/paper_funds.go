package order

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// The caller holds the account lock. Market valuation happens outside the
// SQLite transaction; cash and active reservations are read inside the write.
func (s *Service) paperMarginAdjustment(ctx context.Context, validation Validation) (shared.Decimal, error) {
	if validation.Account.ExecutionMode != exchange.ExecutionModePaper || validation.Account.MarketType != exchange.MarketTypeSwap {
		return shared.Zero(), nil
	}
	if s.Adapters == nil {
		return shared.Zero(), ErrServiceConfig
	}
	adapter, err := s.Adapters.Adapter(validation.Account.ID)
	if err != nil {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "adapter", err)
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "snapshot", err)
	}
	if !snapshot.Present.UsedMargin || !snapshot.Present.UnrealizedPnL || snapshot.UsedMargin.IsNegative() ||
		snapshot.ExchangeUpdatedAt.IsZero() || snapshot.ExchangeUpdatedAt.After(s.now()) || s.now().Sub(snapshot.ExchangeUpdatedAt) > 10*time.Second {
		return shared.Zero(), accountExecutionError(validation.Account.ID, "snapshot", fmt.Errorf("%w: paper margin valuation unavailable", ErrServiceConfig))
	}
	return snapshot.UnrealizedPnL.Sub(snapshot.UsedMargin), nil
}

func checkPaperFunds(tx *store.Tx, validation Validation, marginAdjustment, required shared.Decimal, oldAsset, oldQuantity string) error {
	if validation.ReduceOnly {
		// 减仓单不占用新保证金：已占用的保证金会在平仓时释放，只需现金覆盖手续费预留。
		// 若仍扣除已占用保证金，接近满保证金的仓位会因“可用资金不足”而无法平仓（需要先平仓才能释放保证金）。
		marginAdjustment = shared.Zero()
	}
	if oldAsset == "" && oldQuantity == "" {
		available, err := availableReservationFunds(tx, validation, marginAdjustment)
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
	available := balances.Totals[validation.ReservedAsset].Add(marginAdjustment).Sub(reserved)
	if available.Cmp(required) < 0 {
		return ErrInsufficientFunds
	}
	return nil
}
