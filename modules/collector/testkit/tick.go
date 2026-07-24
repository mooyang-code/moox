// Package testkit exposes deterministic Collector ingress helpers for
// cross-module integration tests. It is not used by the collector runtime.
package testkit

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/mooyang-code/moox/packages/events"
)

type BinanceTick struct {
	ID         int64
	Price      string
	Quantity   string
	TradeTime  time.Time
	BuyerMaker bool
}

type TickParams struct {
	SpaceID   string
	InstType  string
	Symbol    string
	SubjectID string
}

type staticTradeAPI struct {
	trades []*exchange.Trade
}

func (a staticTradeAPI) GetRecentTrades(context.Context, *exchange.TradeRequest) ([]*exchange.Trade, error) {
	return a.trades, nil
}

// PublishBinanceTicks runs the real Binance TickCollector against a
// deterministic exchange response and publishes into the supplied EventBus.
// The caller can therefore connect Collector ingress and downstream services
// to the same NATS stream without bypassing Collector with Publisher.Publish.
func PublishBinanceTicks(ctx context.Context, publisher *events.Publisher, params TickParams, ticks []BinanceTick) error {
	trades := make([]*exchange.Trade, 0, len(ticks))
	for _, tick := range ticks {
		trades = append(trades, &exchange.Trade{
			ID:         tick.ID,
			Price:      common.NewDecimal(tick.Price),
			Quantity:   common.NewDecimal(tick.Quantity),
			TradeTime:  tick.TradeTime,
			BuyerMaker: tick.BuyerMaker,
		})
	}
	return binance.NewTickCollector(staticTradeAPI{trades: trades}, nil, publisher).Collect(ctx, &sources.CollectParams{
		SpaceID: params.SpaceID, InstType: params.InstType, Symbol: params.Symbol, SubjectID: params.SubjectID, Live: true,
	})
}
