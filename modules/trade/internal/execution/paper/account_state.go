package paper

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/reservation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
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
