package holding

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"time"
)

type Holding struct {
	TradingAccountID, InstrumentID, ExchangeSymbol, Asset string
	Quantity, AverageCost, MarkPrice, MarketValue         shared.Decimal
	UnrealizedPnL                                         *shared.Decimal
	SourceTime                                            time.Time
}
type QuoteSource interface {
	Quote(context.Context, string) (shared.Decimal, time.Time, error)
}
type Service struct {
	Store  *store.Store
	Quotes QuoteSource
}

func (s *Service) List(ctx context.Context, spaceID, accountID string) ([]Holding, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("holding: store is not configured")
	}
	account, err := s.Store.GetTradingAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	if account.MarketType != "SPOT" {
		return nil, nil
	}
	var balances []store.AssetBalance = account.Snapshot.Balances
	out := make([]Holding, 0, len(balances))
	for _, b := range balances {
		quantity, err := shared.ParseDecimal(b.Total)
		if err != nil || quantity.IsZero() {
			continue
		}
		h := Holding{TradingAccountID: accountID, Asset: b.Asset, Quantity: quantity, SourceTime: time.UnixMilli(account.Snapshot.ExchangeUpdatedAt)}
		if b.Asset == account.SettlementAsset {
			h.MarkPrice = shared.MustDecimal("1")
			h.MarketValue = quantity
		} else if s.Quotes != nil {
			price, source, qerr := s.Quotes.Quote(ctx, b.Asset)
			if qerr != nil {
				return nil, qerr
			}
			h.MarkPrice, h.MarketValue, h.SourceTime = price, quantity.Mul(price), source
		}
		out = append(out, h)
	}
	return out, nil
}
