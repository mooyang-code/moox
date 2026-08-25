package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
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
	ExecutionEligibility(context.Context, string) (tradingaccount.Account, error)
}

type InstrumentSource interface {
	GetInstrument(context.Context, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error)
}

// AccountInstrumentSource lets production implementations include the
// account's environment in instrument identity without burdening test stubs.
type AccountInstrumentSource interface {
	GetInstrumentForAccount(context.Context, string, exchange.Exchange, exchange.MarketType, string) (exchange.Instrument, error)
}

type PositionSource interface {
	GetPosition(context.Context, string, string) (exchange.Position, error)
}

type AccountPositionSource interface {
	GetPositionForAccount(context.Context, string, string) (exchange.Position, error)
}

type Validation struct {
	Account          tradingaccount.Account
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
	Now              func() time.Time
	MaxReferenceAge  time.Duration
	MaxChildNotional shared.Decimal
	MaxLeverage      shared.Decimal
	FeeBufferRate    shared.Decimal
}

func (v Validator) Validate(
	ctx context.Context,
	spaceID string,
	spec orderdomain.OrderSpec,
) (Validation, error) {
	if v.Accounts == nil || v.Instruments == nil || strings.TrimSpace(spaceID) == "" {
		return Validation{}, ErrValidatorConfig
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	account, err := v.Accounts.ExecutionEligibility(ctx, spec.TradingAccountID)
	if err != nil {
		return Validation{}, err
	}
	if account.SpaceID != spaceID {
		return Validation{}, ErrAccountOwnership
	}
	if err := spec.Validate(account.MarketType, now, v.MaxReferenceAge); err != nil {
		return Validation{}, err
	}
	var instrument exchange.Instrument
	if scoped, ok := v.Instruments.(AccountInstrumentSource); ok {
		instrument, err = scoped.GetInstrumentForAccount(ctx, account.ID, account.Exchange, account.MarketType, spec.InstrumentID)
	} else {
		instrument, err = v.Instruments.GetInstrument(ctx, account.Exchange, account.MarketType, spec.InstrumentID)
	}
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
	if spec.Type == exchange.OrderTypeLimit {
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
	feeRate := v.FeeBufferRate
	if account.ExecutionMode == exchange.ExecutionModePaper && account.Paper != nil {
		// Paper reservations use the configured worst-case fee rather than the
		// process-wide live safety buffer. A first MARKET/LIMIT match may be
		// taker, while a resting GTC LIMIT may later be maker.
		feeRate = maxDecimal(account.Paper.MakerFeeRate, account.Paper.TakerFeeRate)
	}
	switch account.MarketType {
	case exchange.MarketTypeSpot:
		if spec.Side == exchange.SideBuy {
			result.ReservedAsset = instrument.QuoteAsset
			reserveNotional := referenceNotional
			if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeMarket && account.Paper != nil {
				reserveNotional = paperSlippagePrice(referenceNotional, spec.Side, account.Paper.SlippageBPS)
			}
			result.ReservedQuantity = withFeeBuffer(reserveNotional, feeRate)
			if spec.Type == exchange.OrderTypeLimit {
				result.ReservedQuantity = orderNotional
				if account.ExecutionMode == exchange.ExecutionModePaper {
					result.ReservedQuantity = withFeeBuffer(orderNotional, feeRate)
				}
			}
		} else {
			result.ReservedAsset = instrument.BaseAsset
			result.ReservedQuantity = spec.Quantity
		}
		if availableBalance(account.Snapshot, result.ReservedAsset).Cmp(result.ReservedQuantity) < 0 {
			return Validation{}, ErrInsufficientFunds
		}
	case exchange.MarketTypeSwap:
		leverage, found := account.LeverageSettings[spec.InstrumentID]
		if !found && instrument.ExchangeSymbol != "" {
			leverage, found = account.LeverageSettings[instrument.ExchangeSymbol]
		}
		if !found && instrument.Symbol != "" {
			leverage, found = account.LeverageSettings[instrument.Symbol]
		}
		if !found || leverage.Cmp(shared.Zero()) <= 0 {
			return Validation{}, fmt.Errorf("%w: missing symbol leverage", ErrLeverageLimit)
		}
		if v.MaxLeverage.Cmp(shared.Zero()) > 0 && leverage.Cmp(v.MaxLeverage) > 0 {
			return Validation{}, ErrLeverageLimit
		}
		result.Leverage = leverage
		result.ReservedAsset = account.SettlementAsset
		reserveNotional := referenceNotional
		if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeLimit && spec.LimitPrice != nil {
			reserveNotional = orderNotional
		}
		if account.ExecutionMode == exchange.ExecutionModePaper && spec.Type == exchange.OrderTypeMarket && account.Paper != nil {
			reserveNotional = paperSlippagePrice(referenceNotional, spec.Side, account.Paper.SlippageBPS)
		}
		if spec.ReducePositionOnly {
			if err := v.validateReduceOnly(ctx, spec); err != nil {
				return Validation{}, err
			}
			if account.ExecutionMode == exchange.ExecutionModePaper {
				result.ReservedQuantity = reserveNotional.Mul(feeRate)
			}
		} else {
			margin := reserveNotional.Div(leverage)
			result.ReservedQuantity = margin
			if account.ExecutionMode == exchange.ExecutionModePaper {
				result.ReservedQuantity = margin.Add(reserveNotional.Mul(feeRate))
			} else {
				result.ReservedQuantity = withFeeBuffer(margin, feeRate)
			}
		}
		if account.Snapshot.AvailableFunds.Cmp(result.ReservedQuantity) < 0 {
			return Validation{}, ErrInsufficientFunds
		}
	}
	return result, nil
}

func maxDecimal(left, right shared.Decimal) shared.Decimal {
	if left.Cmp(right) >= 0 {
		return left
	}
	return right
}

func paperSlippagePrice(notional shared.Decimal, side exchange.Side, slippage shared.Decimal) shared.Decimal {
	if slippage.Cmp(shared.Zero()) <= 0 {
		return notional
	}
	factor := shared.MustDecimal("1").Add(slippage.Div(shared.MustDecimal("10000")))
	if side == exchange.SideSell {
		factor = shared.MustDecimal("1").Sub(slippage.Div(shared.MustDecimal("10000")))
	}
	return notional.Mul(factor)
}

func (v Validator) validateReduceOnly(ctx context.Context, spec orderdomain.OrderSpec) error {
	if v.Positions == nil {
		return ErrReduceOnly
	}
	var position exchange.Position
	var err error
	if scoped, ok := v.Positions.(AccountPositionSource); ok {
		position, err = scoped.GetPositionForAccount(ctx, spec.TradingAccountID, spec.InstrumentID)
	} else {
		position, err = v.Positions.GetPosition(ctx, spec.TradingAccountID, spec.InstrumentID)
	}
	if err != nil {
		return err
	}
	if position.SignedQuantity.IsZero() ||
		(position.SignedQuantity.Cmp(shared.Zero()) > 0 && spec.Side != exchange.SideSell) ||
		(position.SignedQuantity.Cmp(shared.Zero()) < 0 && spec.Side != exchange.SideBuy) {
		return ErrReduceOnly
	}
	if spec.Quantity.Cmp(position.SignedQuantity.Abs()) > 0 {
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
