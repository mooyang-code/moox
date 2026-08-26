package reservation

import (
	"errors"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var ErrInsufficientReducibleQuantity = errors.New("trade reservation: insufficient reducible quantity")

type Facts struct {
	AvailableByAsset           map[string]shared.Decimal
	AvailableFunds             shared.Decimal
	SignedPositionQuantity     shared.Decimal
	AvailableReducibleQuantity shared.Decimal
	Leverage                   shared.Decimal
}

type Reservation struct {
	Asset               string
	Quantity            shared.Decimal
	PaperExecutionPrice *shared.Decimal
	FirstMatchPending   bool
}
