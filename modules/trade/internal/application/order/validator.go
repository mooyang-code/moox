package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrAccountOwnership   = errors.New("trade order: Exchange account ownership mismatch")
	ErrInstrumentDisabled = errors.New("trade order: instrument is not tradable")
	ErrQuantityRule       = errors.New("trade order: quantity violates instrument rules")
	ErrNotionalLimit      = errors.New("trade order: maximum child notional exceeded")
	ErrInsufficientFunds  = errors.New("trade order: insufficient available funds")
	ErrLeverageLimit      = errors.New("trade order: leverage exceeds configured ceiling")
	ErrReduceOnly         = errors.New("trade order: invalid reduce-only direction")
	ErrValidatorConfig    = errors.New("trade order: validator is not configured")
)

type AccountEligibility interface {
	ExecutionEligibility(context.Context, string) (exchangeaccount.Account, error)
}

type InstrumentSource interface {
	GetInstrument(context.Context, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error)
}

type PositionSource interface {
	GetPosition(context.Context, string, string) (exchange.Position, error)
}

type Validation struct {
	Account          exchangeaccount.Account
	Instrument       exchange.Instrument
	Notional         shared.Decimal
	Leverage         shared.Decimal
	ReservedAsset    string
	ReservedQuantity shared.Decimal
}

type Validator struct {
	Accounts         AccountEligibility
	Instruments      InstrumentSource
	Positions        PositionSource
	SpaceID          string
	Now              func() time.Time
	MaxReferenceAge  time.Duration
	MaxChildNotional shared.Decimal
	MaxLeverage      shared.Decimal
	FeeBufferRate    shared.Decimal
}

func (v Validator) Validate(
	ctx context.Context,
	spec orderdomain.OrderSpec,
) (Validation, error) {
	if v.Accounts == nil || v.Instruments == nil || strings.TrimSpace(v.SpaceID) == "" {
		return Validation{}, ErrValidatorConfig
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	account, err := v.Accounts.ExecutionEligibility(ctx, spec.ExchangeAccountID)
	if err != nil {
		return Validation{}, err
	}
	if account.SpaceID != v.SpaceID {
		return Validation{}, ErrAccountOwnership
	}
	if err := spec.Validate(account.MarketType, now, v.MaxReferenceAge); err != nil {
		return Validation{}, err
	}
	instrument, err := v.Instruments.GetInstrument(
		ctx,
		account.Exchange,
		account.MarketType,
		spec.Symbol,
	)
	if err != nil {
		return Validation{}, err
	}
	if instrument.Status != "TRADING" && instrument.Status != "live" {
		return Validation{}, ErrInstrumentDisabled
	}
	exchangeQuantity := spec.Quantity
	if account.MarketType == exchange.MarketTypeSwap {
		if instrument.ContractValue.Cmp(shared.Zero()) <= 0 {
			return Validation{}, ErrQuantityRule
		}
		exchangeQuantity = spec.Quantity.Div(instrument.ContractValue)
	}
	if !validQuantity(
		exchangeQuantity,
		instrument.ExchangeQuantityStep,
		instrument.MinExchangeQuantity,
	) {
		return Validation{}, ErrQuantityRule
	}

	referenceNotional := spec.Quantity.Mul(spec.ReferencePrice)
	orderNotional := referenceNotional
	if spec.OrderType == exchange.OrderTypeLimit {
		orderNotional = spec.Quantity.Mul(*spec.LimitPrice)
	}
	if instrument.MinNotional.Cmp(shared.Zero()) > 0 &&
		orderNotional.Cmp(instrument.MinNotional) < 0 {
		return Validation{}, ErrQuantityRule
	}
	if v.MaxChildNotional.Cmp(shared.Zero()) > 0 &&
		referenceNotional.Cmp(v.MaxChildNotional) > 0 {
		return Validation{}, ErrNotionalLimit
	}

	result := Validation{
		Account: account, Instrument: instrument, Notional: referenceNotional,
		Leverage: shared.MustDecimal("1"),
	}
	switch account.MarketType {
	case exchange.MarketTypeSpot:
		if spec.Side == exchange.SideBuy {
			result.ReservedAsset = instrument.QuoteAsset
			result.ReservedQuantity = withFeeBuffer(referenceNotional, v.FeeBufferRate)
			if spec.OrderType == exchange.OrderTypeLimit {
				result.ReservedQuantity = orderNotional
			}
		} else {
			result.ReservedAsset = instrument.BaseAsset
			result.ReservedQuantity = spec.Quantity
		}
		if availableBalance(account.Snapshot, result.ReservedAsset).Cmp(result.ReservedQuantity) < 0 {
			return Validation{}, ErrInsufficientFunds
		}
	case exchange.MarketTypeSwap:
		leverage, found := account.LeverageSettings[spec.Symbol]
		if !found || leverage.Cmp(shared.Zero()) <= 0 {
			return Validation{}, fmt.Errorf("%w: missing symbol leverage", ErrLeverageLimit)
		}
		if v.MaxLeverage.Cmp(shared.Zero()) > 0 && leverage.Cmp(v.MaxLeverage) > 0 {
			return Validation{}, ErrLeverageLimit
		}
		result.Leverage = leverage
		result.ReservedAsset = account.SettlementAsset
		if spec.ReduceOnly {
			if err := v.validateReduceOnly(ctx, spec); err != nil {
				return Validation{}, err
			}
		} else {
			result.ReservedQuantity = withFeeBuffer(
				referenceNotional.Div(leverage),
				v.FeeBufferRate,
			)
			if account.Snapshot.AvailableFunds.Cmp(result.ReservedQuantity) < 0 {
				return Validation{}, ErrInsufficientFunds
			}
		}
	}
	return result, nil
}

func (v Validator) validateReduceOnly(ctx context.Context, spec orderdomain.OrderSpec) error {
	if v.Positions == nil {
		return ErrReduceOnly
	}
	position, err := v.Positions.GetPosition(ctx, spec.ExchangeAccountID, spec.Symbol)
	if err != nil {
		return err
	}
	if position.SignedQuantity.IsZero() ||
		(position.SignedQuantity.Cmp(shared.Zero()) > 0 && spec.Side != exchange.SideSell) ||
		(position.SignedQuantity.Cmp(shared.Zero()) < 0 && spec.Side != exchange.SideBuy) ||
		spec.Quantity.Cmp(position.SignedQuantity.Abs()) > 0 {
		return ErrReduceOnly
	}
	return nil
}

func validQuantity(quantity, step, minimum shared.Decimal) bool {
	if step.Cmp(shared.Zero()) <= 0 || quantity.Cmp(minimum) < 0 {
		return false
	}
	return quantity.Div(step).IsInteger()
}

func withFeeBuffer(amount, rate shared.Decimal) shared.Decimal {
	if rate.Cmp(shared.Zero()) <= 0 {
		return amount
	}
	return amount.Add(amount.Mul(rate))
}

func availableBalance(snapshot exchange.AccountSnapshot, asset string) shared.Decimal {
	for _, balance := range snapshot.Balances {
		if balance.Asset == asset {
			return balance.Available
		}
	}
	return shared.Zero()
}
