package execution

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type LegacyAdapter struct{ Adapter exchange.Adapter }

func (a LegacyAdapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	return a.Adapter.GetAccountSnapshot(ctx)
}
func (a LegacyAdapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	return a.Adapter.ListPositionSnapshots(ctx)
}
func (a LegacyAdapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	return a.Adapter.ListOpenOrders(ctx)
}
func (a LegacyAdapter) ListRecentFills(ctx context.Context, symbol shared.ExchangeSymbol, cursor string) ([]exchange.Fill, string, error) {
	return a.Adapter.ListRecentFills(ctx, symbol.String(), cursor)
}
func (a LegacyAdapter) GetOrder(ctx context.Context, symbol shared.ExchangeSymbol, id string) (exchange.Order, error) {
	return a.Adapter.GetOrder(ctx, symbol.String(), id)
}
func (a LegacyAdapter) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (exchange.Order, error) {
	return a.Adapter.PlaceOrder(ctx, req)
}
func (a LegacyAdapter) CancelOrder(ctx context.Context, symbol shared.ExchangeSymbol, id string) (exchange.Order, error) {
	return a.Adapter.CancelOrder(ctx, symbol.String(), id)
}
func (a LegacyAdapter) SetLeverage(ctx context.Context, symbol shared.ExchangeSymbol, value shared.Decimal) error {
	return a.Adapter.SetLeverage(ctx, symbol.String(), value)
}
func (a LegacyAdapter) SetMarginMode(ctx context.Context, symbol shared.ExchangeSymbol, mode exchange.MarginMode) error {
	return a.Adapter.SetMarginMode(ctx, symbol.String(), mode)
}
