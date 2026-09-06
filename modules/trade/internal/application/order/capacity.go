package order

import (
	"context"
	"errors"
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
	if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
		spec, err = s.paperReference(ctx, spec, validation)
		if err != nil {
			return shared.Zero(), err
		}
		validation, err = s.Validator.validateOptions(ctx, spaceID, spec, true, true)
		if err != nil {
			return shared.Zero(), err
		}
	}
	adjustment, err := s.paperMarginAdjustment(ctx, validation)
	if err != nil {
		return shared.Zero(), err
	}
	var available shared.Decimal
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		var err error
		available, err = availableReservationFunds(tx, validation, adjustment)
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
	if validation.Account.MarketType == exchange.MarketTypeSwap {
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
	price := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit {
		price = *spec.LimitPrice
	}
	if quantity.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 || quantity.Mul(price).Cmp(validation.Instrument.MinNotional) < 0 {
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

func availableReservationFunds(tx *store.Tx, validation Validation, adjustment shared.Decimal) (shared.Decimal, error) {
	if validation.Account.ExecutionMode == exchange.ExecutionModePaper {
		balances, err := tx.GetPaperBalanceSnapshot(validation.Account.SpaceID, validation.Account.ID)
		if err != nil {
			return shared.Zero(), err
		}
		return balances.Totals[validation.ReservedAsset].Add(adjustment).Sub(balances.Reserved[validation.ReservedAsset]), nil
	}
	unreflected, err := tx.GetUnreflectedReservation(validation.Account.SpaceID, validation.Account.ID, validation.ReservedAsset, validation.Account.LastSyncAt.UnixMilli())
	if err != nil {
		return shared.Zero(), err
	}
	available := availableBalance(validation.Account.Snapshot, validation.ReservedAsset)
	if validation.Account.MarketType == exchange.MarketTypeSwap {
		available = validation.Account.Snapshot.AvailableFunds
	}
	return available.Sub(unreflected), nil
}

func (s *Service) paperReference(ctx context.Context, spec orderdomain.OrderSpec, validation Validation) (orderdomain.OrderSpec, error) {
	if s.Adapters == nil {
		return spec, ErrServiceConfig
	}
	adapter, err := s.Adapters.Adapter(spec.TradingAccountID)
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "adapter", err)
	}
	marketData, ok := adapter.(execution.MarketDataSource)
	if !ok {
		return spec, accountExecutionError(spec.TradingAccountID, "quote", errors.New("trade order: paper market data source is unavailable"))
	}
	quote, err := marketData.GetQuote(ctx, shared.ExchangeSymbol(validation.Instrument.ExchangeSymbol))
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "quote", err)
	}
	if !paperQuoteFresh(quote, s.now(), 10*time.Second) {
		return spec, accountExecutionError(spec.TradingAccountID, "quote", errors.New("trade order: paper quote is stale"))
	}
	price, err := paperExecutablePrice(spec.Side, quote)
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "quote", err)
	}
	spec.ReferencePrice, spec.ReferencePriceAt = price, quote.SourceTime
	return spec, nil
}
