// Package contracttest provides the behavioral checks shared by every
// account-bound Exchange adapter.
package contracttest

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type PlaceFunc func(context.Context, exchange.OrderRequest) (exchange.Order, error)

func RunRequestValidation(t *testing.T, market exchange.MarketType, place PlaceFunc) {
	t.Helper()
	base := exchange.OrderRequest{
		ClientOrderID: "contract-client",
		Symbol:        "BTCUSDT",
		OrderType:     exchange.OrderTypeMarket,
		Side:          exchange.SideBuy,
		Quantity:      shared.MustDecimal("1"),
	}
	if market == exchange.MarketTypeSwap {
		base.PositionSide = exchange.PositionSideNet
	}
	price := shared.MustDecimal("100")
	tests := []struct {
		name   string
		mutate func(*exchange.OrderRequest)
	}{
		{
			name: "MARKET rejects price",
			mutate: func(request *exchange.OrderRequest) {
				request.LimitPrice = &price
			},
		},
		{
			name: "MARKET rejects time in force",
			mutate: func(request *exchange.OrderRequest) {
				request.TimeInForce = exchange.TimeInForceGTC
			},
		},
		{
			name: "LIMIT requires price",
			mutate: func(request *exchange.OrderRequest) {
				request.OrderType = exchange.OrderTypeLimit
				request.TimeInForce = exchange.TimeInForceGTC
			},
		},
	}
	if market == exchange.MarketTypeSpot {
		tests = append(tests, struct {
			name   string
			mutate func(*exchange.OrderRequest)
		}{
			name: "SPOT rejects reduce only",
			mutate: func(request *exchange.OrderRequest) {
				request.ReduceOnly = true
			},
		})
	} else {
		tests = append(tests, struct {
			name   string
			mutate func(*exchange.OrderRequest)
		}{
			name: "SWAP requires NET position side",
			mutate: func(request *exchange.OrderRequest) {
				request.PositionSide = exchange.PositionSideUnspecified
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			if _, err := place(context.Background(), request); !exchange.IsKind(
				err,
				exchange.ErrorRejected,
			) {
				t.Fatalf("PlaceOrder() error = %v, want REJECTED", err)
			}
		})
	}
}
