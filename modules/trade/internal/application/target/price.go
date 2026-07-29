package target

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type AdapterSource interface {
	Adapter(exchangeAccountID string) (exchange.Adapter, error)
}

type ExchangePriceSource struct {
	Adapters AdapterSource
}

func (s ExchangePriceSource) LatestPrice(
	ctx context.Context,
	exchangeAccountID string,
	symbol string,
) (Quote, error) {
	if s.Adapters == nil {
		return Quote{}, ErrExecutorConfig
	}
	adapter, err := s.Adapters.Adapter(exchangeAccountID)
	if err != nil {
		return Quote{}, err
	}
	source, ok := adapter.(exchange.ReferencePriceSource)
	if !ok {
		return Quote{}, fmt.Errorf(
			"%w: Exchange adapter has no reference price source",
			ErrExecutorConfig,
		)
	}
	quote, err := source.GetReferencePrice(ctx, symbol)
	if err != nil {
		return Quote{}, err
	}
	return Quote{Price: quote.Price, UpdatedAt: quote.UpdatedAt}, nil
}
