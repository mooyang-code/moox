package order

import (
	"context"
	"errors"
	"math/big"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
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
	quantity = stockBuyCapacity(validation, spec, quantity, step, available)
	price := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit {
		price = *spec.LimitPrice
	}
	if quantity.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 || quantity.Mul(price).Cmp(validation.Instrument.MinNotionalAt(price)) < 0 {
		return shared.Zero(), nil
	}
	return quantity, nil
}

// stockBuyCapacity 给模拟盘 A 股买单把容量收到费用也付得起：佣金有最低 5 元，花费和数量不成正比，
// 按比例算出的数量可能差几块钱，这里按 StockFee 逐个步长往下减到付得起（和预留、扣费用同一个函数）。
func stockBuyCapacity(validation Validation, spec orderdomain.OrderSpec, quantity, step, available shared.Decimal) shared.Decimal {
	account := validation.Account
	if spec.Side != exchange.SideBuy || account.ExecutionMode != exchange.ExecutionModePaper || account.Paper == nil ||
		!exchange.RuleFor(validation.Instrument.MarketType).StockFees || validation.ReservedQuantity.IsZero() {
		return quantity
	}
	unit := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit && spec.LimitPrice != nil {
		unit = *spec.LimitPrice
	} else {
		unit = paperSlippagePrice(unit, spec.Side, account.Paper.SlippageBPS)
	}
	for quantity.Cmp(shared.Zero()) > 0 &&
		exchange.StockBuyCost(quantity.Mul(unit), account.Paper.TakerFeeRate).Cmp(available) > 0 {
		quantity = quantity.Sub(step)
	}
	if quantity.IsNegative() {
		return shared.Zero()
	}
	return quantity
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
		t1, err := paperT1Locked(tx, validation)
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

// paperT1Locked 是模拟盘卖单当天还不能卖的数量：T+1 市场（股票）当日买入的部分。买单和其他市场返回 0。
func paperT1Locked(tx *store.Tx, validation Validation) (shared.Decimal, error) {
	instrument := validation.Instrument
	if !exchange.RuleFor(instrument.MarketType).T1 || validation.ReservedAsset == "" || validation.ReservedAsset != instrument.BaseAsset {
		return shared.Zero(), nil
	}
	bought, err := tx.PaperT1Bought(validation.Account.SpaceID, validation.Account.ID, exchange.T1Since(time.Now()).UnixMilli())
	if err != nil {
		return shared.Zero(), err
	}
	return bought[validation.ReservedAsset], nil
}

func (s *Service) paperReference(ctx context.Context, spec orderdomain.OrderSpec, validation Validation) (orderdomain.OrderSpec, error) {
	if s.Adapters == nil {
		return spec, ErrServiceConfig
	}
	adapter, err := s.Adapters.Adapter(spec.TradingAccountID)
	if err != nil {
		return spec, accountExecutionError(spec.TradingAccountID, "adapter", err)
	}
	quote, err := adapter.GetQuote(ctx, exchange.NewSymbolKey(validation.Instrument.MarketType, validation.Instrument.ExchangeSymbol))
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
