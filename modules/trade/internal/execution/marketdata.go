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
	GetQuote(context.Context, shared.ExchangeSymbol) (MarketQuote, error)
}

type ReferencePriceSource interface {
	GetReferencePrice(context.Context, string) (exchange.ReferencePrice, error)
}
