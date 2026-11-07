package target

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

type ExchangePriceSource struct {
	Adapters AdapterSource
}

func (s ExchangePriceSource) LatestPrice(
	ctx context.Context,
	tradingAccountID string,
	key exchange.SymbolKey,
) (Quote, error) {
	if s.Adapters == nil {
		return Quote{}, ErrExecutorConfig
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return Quote{}, err
	}
	quote, err := adapter.GetReferencePrice(ctx, key)
	if err != nil {
		return Quote{}, err
	}
	return Quote{Price: quote.Price, UpdatedAt: quote.UpdatedAt}, nil
}
