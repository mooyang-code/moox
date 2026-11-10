package reservation

import "github.com/mooyang-code/moox/modules/trade/internal/domain/shared"

type Reservation struct {
	Asset               string
	Quantity            shared.Decimal
	PaperExecutionPrice *shared.Decimal
	FirstMatchPending   bool
}
