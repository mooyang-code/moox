package exchange

import (
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// ReferencePrice is the normalized quote used by validation and holdings.
// Market-data access belongs to execution; this value type remains part of
// the exchange domain model so adapters and callers share one representation.
type ReferencePrice struct {
	Price     shared.Decimal
	UpdatedAt time.Time
}
