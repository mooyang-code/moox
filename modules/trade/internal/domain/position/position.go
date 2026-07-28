package position

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var ErrInvalidPosition = errors.New("trade: invalid Exchange position")

type Position struct {
	ExchangeAccountID string
	Symbol            string
	PositionSide      exchange.PositionSide
	SignedQuantity    shared.Decimal
	EntryPrice        shared.Decimal
	MarkPrice         shared.Decimal
	Leverage          shared.Decimal
	MarginMode        exchange.MarginMode
	UsedMargin        shared.Decimal
	LiquidationPrice  shared.Decimal
	UnrealizedPnL     shared.Decimal
	RealizedPnL       shared.Decimal
	ExchangeUpdatedAt time.Time
}

func (p Position) Validate() error {
	if strings.TrimSpace(p.ExchangeAccountID) == "" ||
		strings.TrimSpace(p.Symbol) == "" ||
		p.PositionSide != exchange.PositionSideNet ||
		p.Leverage.Cmp(shared.Zero()) <= 0 ||
		p.MarginMode != exchange.MarginModeCross ||
		p.EntryPrice.IsNegative() ||
		p.MarkPrice.IsNegative() ||
		p.UsedMargin.IsNegative() ||
		p.LiquidationPrice.IsNegative() {
		return fmt.Errorf("%w: invalid NET snapshot", ErrInvalidPosition)
	}
	return nil
}
