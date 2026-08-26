package paper

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/reservation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type PositionState struct{ Quantity, EntryPrice, MarkPrice, RealizedPnL shared.Decimal }
type AccountState struct {
	SettlementAsset string
	Balances        map[string]shared.Decimal
	Positions       map[string]PositionState
	CumulativeFee   shared.Decimal
	RealizedPnL     shared.Decimal
	Reservations    []reservation.Reservation
}

func Rebuild(account tradingaccount.Account, instruments []exchange.Instrument, fills []exchange.Fill, activeOrders []store.OrderRecord) (AccountState, error) {
	if account.Paper == nil {
		return AccountState{}, fmt.Errorf("paper: paper config is required")
	}
	s := AccountState{SettlementAsset: account.SettlementAsset, Balances: map[string]shared.Decimal{account.SettlementAsset: account.Paper.InitialBalance}, Positions: map[string]PositionState{}, CumulativeFee: shared.Zero(), RealizedPnL: shared.Zero()}
	for _, fill := range fills {
		key := fill.ExchangeSymbol
		position := s.Positions[key]
		qty := fill.Quantity
		if fill.Side == exchange.SideSell {
			qty = qty.Neg()
		}
		position.Quantity = position.Quantity.Add(qty)
		if !position.Quantity.IsZero() {
			position.EntryPrice = fill.Price
		}
		position.MarkPrice = fill.Price
		position.RealizedPnL = position.RealizedPnL.Add(fill.RealizedPnL)
		s.Positions[key] = position
		fee := fill.Fee
		s.CumulativeFee = s.CumulativeFee.Add(fee)
		s.RealizedPnL = s.RealizedPnL.Add(fill.RealizedPnL)
		cash := fill.Price.Mul(fill.Quantity)
		if fill.Side == exchange.SideBuy {
			s.Balances[instrumentQuote(instruments, key)] = s.Balances[instrumentQuote(instruments, key)].Sub(cash).Sub(fee)
		} else {
			s.Balances[instrumentQuote(instruments, key)] = s.Balances[instrumentQuote(instruments, key)].Add(cash).Sub(fee)
		}
	}
	for _, order := range activeOrders {
		if order.ReservedAsset != "" {
			reserved, err := shared.ParseDecimal(order.RemainingReservedQuantity)
			if err != nil {
				return AccountState{}, fmt.Errorf("paper: invalid order reservation: %w", err)
			}
			s.Reservations = append(s.Reservations, reservation.Reservation{Asset: order.ReservedAsset, Quantity: reserved})
		}
	}
	return s, nil
}

func instrumentQuote(instruments []exchange.Instrument, symbol string) string {
	for _, i := range instruments {
		if i.ExchangeSymbol == symbol {
			return i.QuoteAsset
		}
	}
	return "USDT"
}
