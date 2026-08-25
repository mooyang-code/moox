package paper

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// LegacyAdapter is the narrow bridge kept while the runtime session still
// exposes exchange.Adapter to the older sync/order services. Paper execution
// itself is backed by Adapter and the SQLite matcher; this bridge never
// synthesizes fills or executes an order synchronously.
type LegacyAdapter struct{ Adapter *Adapter }

func (a *LegacyAdapter) Exchange() exchange.Exchange { return a.Adapter.AccountExchange() }
func (a *LegacyAdapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return a.Adapter.LoadInstruments(ctx)
}
func (a *LegacyAdapter) GetReferencePrice(ctx context.Context, symbol string) (exchange.ReferencePrice, error) {
	return a.Adapter.GetReferencePrice(ctx, symbol)
}
func (a *LegacyAdapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	return a.Adapter.GetAccountSnapshot(ctx)
}
func (a *LegacyAdapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	return a.Adapter.ListPositionSnapshots(ctx)
}
func (a *LegacyAdapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	return a.Adapter.ListOpenOrders(ctx)
}
func (a *LegacyAdapter) ListRecentFills(ctx context.Context, symbol, cursor string) ([]exchange.Fill, string, error) {
	return a.Adapter.ListRecentFills(ctx, shared.ExchangeSymbol(symbol), cursor)
}
func (a *LegacyAdapter) GetOrder(ctx context.Context, symbol, clientID string) (exchange.Order, error) {
	return a.Adapter.GetOrder(ctx, shared.ExchangeSymbol(symbol), clientID)
}
func (a *LegacyAdapter) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.Order, error) {
	return a.Adapter.PlaceOrder(ctx, req)
}
func (a *LegacyAdapter) CancelOrder(ctx context.Context, symbol, id string) (exchange.Order, error) {
	return a.Adapter.CancelOrder(ctx, shared.ExchangeSymbol(symbol), id)
}
func (a *LegacyAdapter) SetLeverage(ctx context.Context, symbol string, value shared.Decimal) error {
	return a.Adapter.SetLeverage(ctx, shared.ExchangeSymbol(symbol), value)
}
func (a *LegacyAdapter) SetMarginMode(ctx context.Context, symbol string, mode exchange.MarginMode) error {
	return a.Adapter.SetMarginMode(ctx, shared.ExchangeSymbol(symbol), mode)
}
func (a *LegacyAdapter) Subscribe(ctx context.Context, handler exchange.EventHandler) error {
	exchange.NotifyPrivateReady(handler)
	<-ctx.Done()
	return ctx.Err()
}

func (a *Adapter) AccountExchange() exchange.Exchange {
	return exchange.Exchange(a.Account.Exchange)
}
