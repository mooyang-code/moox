package order

import (
	"context"
	"math/big"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// Capacity returns a read-only, base-quantity estimate capped by spec.Quantity.
// Place still performs authoritative admission under the account lock.
func (s *Service) Capacity(ctx context.Context, spaceID string, spec orderdomain.OrderSpec) (shared.Decimal, error) {
	if s == nil || s.Store == nil {
		return shared.Zero(), ErrServiceConfig
	}
	unlock, err := s.Store.LockTradingAccountContext(ctx, spec.TradingAccountID)
	if err != nil {
		return shared.Zero(), accountExecutionError(spec.TradingAccountID, "capacity_lock", err)
	}
	defer unlock()
	validation, err := s.Validator.validateOptions(ctx, spaceID, spec, true, true)
	if err != nil {
		return shared.Zero(), err
	}
	policy := orderPolicyOf(validation, nil)
	if policy.NeedsExecutableQuote() {
		spec, err = s.adapterExecutableReference(ctx, spec, validation)
		if err != nil {
			return shared.Zero(), err
		}
		validation, err = s.Validator.validateOptions(ctx, spaceID, spec, true, true)
		if err != nil {
			return shared.Zero(), err
		}
		policy = orderPolicyOf(validation, nil)
	}
	adjustment, err := s.paperMarginAdjustment(ctx, validation)
	if err != nil {
		return shared.Zero(), err
	}
	var available shared.Decimal
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var err error
		available, err = availableReservationFunds(tx, validation, adjustment, s.now())
		return err
	})
	if err != nil {
		return shared.Zero(), err
	}
	quantity := spec.Quantity
	if !validation.ReservedQuantity.IsZero() {
		if available.Cmp(shared.Zero()) <= 0 {
			return shared.Zero(), nil
		}
		quantity = minCapacity(quantity, spec.Quantity.Mul(available).Div(validation.ReservedQuantity))
	}
	if s.Validator.MaxChildNotional.Cmp(shared.Zero()) > 0 {
		quantity = minCapacity(quantity, s.Validator.MaxChildNotional.Div(spec.ReferencePrice))
	}
	step, minimum := validation.Instrument.ExchangeQuantityStep, validation.Instrument.MinExchangeQuantity
	if exchange.RuleFor(validation.Instrument.MarketType).ContractValue {
		step = step.Mul(validation.Instrument.ContractValue)
		minimum = minimum.Mul(validation.Instrument.ContractValue)
	}
	if step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), ErrQuantityRule
	}
	// Decimal division is exact rational arithmetic; String retains recurring
	// quotients as fractions, so integer truncation cannot round across a step.
	units, ok := new(big.Rat).SetString(quantity.Div(step).String())
	if !ok {
		return shared.Zero(), ErrQuantityRule
	}
	whole := new(big.Int).Quo(units.Num(), units.Denom())
	quantity = shared.MustDecimal(whole.String()).Mul(step)
	if !validation.ReservedQuantity.IsZero() {
		quantity = policy.ClampBuyCapacity(execution.CapacityClampRequest{
			Account:        validation.Account,
			Instrument:     validation.Instrument,
			OrderType:      spec.Type,
			Side:           spec.Side,
			ReferencePrice: spec.ReferencePrice,
			LimitPrice:     spec.LimitPrice,
			Quantity:       quantity,
			Step:           step,
			Available:      available,
		})
	}
	price := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit {
		price = *spec.LimitPrice
	}
	if quantity.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 || quantity.Mul(price).Cmp(validation.Instrument.MinNotionalAt(price)) < 0 {
		return shared.Zero(), nil
	}
	return quantity, nil
}

func minCapacity(a, b shared.Decimal) shared.Decimal {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

func availableReservationFunds(tx *store.Tx, validation Validation, adjustment shared.Decimal, now time.Time) (shared.Decimal, error) {
	if orderPolicyOf(validation, nil).UsesLocalFundsLedger() {
		balances, err := tx.GetPaperBalanceSnapshot(validation.Account.SpaceID, validation.Account.ID)
		if err != nil {
			return shared.Zero(), err
		}
		t1, err := paperT1Locked(tx, validation, now)
		if err != nil {
			return shared.Zero(), err
		}
		return balances.Totals[validation.ReservedAsset].Add(adjustment).Sub(balances.Reserved[validation.ReservedAsset]).Sub(t1), nil
	}
	market := validation.Instrument.MarketType
	unreflected, err := tx.GetUnreflectedReservation(
		validation.Account.SpaceID, validation.Account.ID, validation.ReservedAsset,
		validation.Account.MarketsSharingFunds(market),
		validation.Account.SnapshotSourceTime.UnixMilli(),
	)
	if err != nil {
		return shared.Zero(), err
	}
	return availableFunds(validation.Account, market, validation.ReservedAsset).Sub(unreflected), nil
}

// paperT1Locked 是模拟盘卖单当天还不能卖的数量：T+1 的场所（A 股）当日买入的部分。买单和其他场所（港股 T+0、加密货币）返回 0。
func paperT1Locked(tx *store.Tx, validation Validation, now time.Time) (shared.Decimal, error) {
	instrument := validation.Instrument
	if !exchange.RuleAt(validation.Account.Exchange, instrument.MarketType).T1 || validation.ReservedAsset == "" || validation.ReservedAsset != instrument.BaseAsset {
		return shared.Zero(), nil
	}
	bought, err := tx.PaperT1Bought(validation.Account.SpaceID, validation.Account.ID, exchange.T1Since(now).UnixMilli())
	if err != nil {
		return shared.Zero(), err
	}
	return bought[validation.ReservedAsset], nil
}

func (s *Service) adapterExecutableReference(ctx context.Context, spec orderdomain.OrderSpec, validation Validation) (orderdomain.OrderSpec, error) {
	if s.Adapters == nil {
		return spec, ErrServiceConfig
	}
	adapter, err := s.Adapters.Adapter(spec.TradingAccountID)
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "adapter", err)
	}
	policy := execution.ResolveOrderPolicy(adapter, validation.Account)
	price, sourceTime, err := policy.ResolveExecutableQuote(
		ctx,
		exchange.NewSymbolKey(validation.Instrument.MarketType, validation.Instrument.ExchangeSymbol),
		spec.Side,
		s.now(),
		10*time.Second,
	)
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "quote", err)
	}
	spec.ReferencePrice, spec.ReferencePriceAt = price, sourceTime
	return spec, nil
}
