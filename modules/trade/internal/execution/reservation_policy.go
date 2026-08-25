package execution

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/reservation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type ReservationPolicy interface {
	Evaluate(tradingaccount.Account, exchange.Instrument, order.OrderSpec, MarketQuote, reservation.Facts) (reservation.Reservation, error)
}

type LiveReservationPolicy struct{}

func (LiveReservationPolicy) Evaluate(_ tradingaccount.Account, instrument exchange.Instrument, spec order.OrderSpec, _ MarketQuote, facts reservation.Facts) (reservation.Reservation, error) {
	if spec.ReducePositionOnly && spec.Quantity.Cmp(facts.AvailableReducibleQuantity) > 0 {
		return reservation.Reservation{}, reservation.ErrInsufficientReducibleQuantity
	}
	asset := instrument.QuoteAsset
	quantity := spec.Quantity.Mul(spec.ReferencePrice)
	if spec.Side == exchange.SideSell {
		asset, quantity = instrument.BaseAsset, spec.Quantity
	}
	return reservation.Reservation{Asset: asset, Quantity: quantity}, nil
}

type PaperReservationPolicy struct{}

func (PaperReservationPolicy) Evaluate(account tradingaccount.Account, instrument exchange.Instrument, spec order.OrderSpec, quote MarketQuote, facts reservation.Facts) (reservation.Reservation, error) {
	if spec.ReducePositionOnly && spec.Quantity.Cmp(facts.AvailableReducibleQuantity) > 0 {
		return reservation.Reservation{}, reservation.ErrInsufficientReducibleQuantity
	}
	price := spec.ReferencePrice
	if spec.Type == exchange.OrderTypeLimit && spec.LimitPrice != nil {
		price = *spec.LimitPrice
	}
	if price.IsZero() {
		price = quote.Last
	}
	fee := shared.Zero()
	if account.Paper != nil {
		fee = account.Paper.TakerFeeRate
	}
	asset := instrument.QuoteAsset
	quantity := spec.Quantity.Mul(price).Mul(shared.MustDecimal("1").Add(fee))
	if spec.Side == exchange.SideSell {
		asset, quantity = instrument.BaseAsset, spec.Quantity
	}
	return reservation.Reservation{Asset: asset, Quantity: quantity, PaperExecutionPrice: &price, FirstMatchPending: spec.Type == exchange.OrderTypeLimit && spec.FillPolicy == exchange.FillPolicyGTC}, nil
}
