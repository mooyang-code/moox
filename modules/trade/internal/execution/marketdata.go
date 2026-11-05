package execution

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type MarketQuote struct {
	Bid        shared.Decimal
	Ask        shared.Decimal
	Last       shared.Decimal
	SourceTime time.Time
}

type MarketDataSource interface {
	LoadInstruments(context.Context) ([]exchange.Instrument, error)
	GetQuote(context.Context, exchange.SymbolKey) (MarketQuote, error)
}

type ReferencePriceSource interface {
	GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error)
}
