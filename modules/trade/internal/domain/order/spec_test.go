package order

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestOrderSpecValidationMatrix(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	price := shared.MustDecimal("100")
	base := OrderSpec{
		ClientOrderSpec: ClientOrderSpec{
			ExchangeAccountID: "account-1", ClientOrderID: "client-1",
			InstrumentID: "BTC-USDT", Side: exchange.SideBuy,
			Quantity: shared.MustDecimal("0.25"),
		},
		ReferencePrice:   price,
		ReferencePriceAt: now.Add(-time.Second),
		Owner:            OrderOwner{Type: "RPC"},
	}

	tests := []struct {
		name   string
		market exchange.MarketType
		mutate func(*OrderSpec)
	}{
		{
			name:   "SPOT MARKET",
			market: exchange.MarketTypeSpot,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeMarket
			},
		},
		{
			name:   "SPOT LIMIT GTC",
			market: exchange.MarketTypeSpot,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeLimit
				spec.FillPolicy = exchange.FillPolicyGTC
				spec.LimitPrice = &price
			},
		},
		{
			name:   "SPOT LIMIT IOC",
			market: exchange.MarketTypeSpot,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeLimit
				spec.FillPolicy = exchange.FillPolicyIOC
				spec.LimitPrice = &price
			},
		},
		{
			name:   "SPOT LIMIT FOK",
			market: exchange.MarketTypeSpot,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeLimit
				spec.FillPolicy = exchange.FillPolicyFOK
				spec.LimitPrice = &price
			},
		},
		{
			name:   "SWAP MARKET",
			market: exchange.MarketTypeSwap,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeMarket
				spec.PositionSide = exchange.PositionSideNet
			},
		},
		{
			name:   "SWAP LIMIT",
			market: exchange.MarketTypeSwap,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeLimit
				spec.FillPolicy = exchange.FillPolicyGTC
				spec.PositionSide = exchange.PositionSideNet
				spec.LimitPrice = &price
			},
		},
		{
			name:   "SWAP reduce-only MARKET",
			market: exchange.MarketTypeSwap,
			mutate: func(spec *OrderSpec) {
				spec.Type = exchange.OrderTypeMarket
				spec.PositionSide = exchange.PositionSideNet
				spec.ReducePositionOnly = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := base
			tt.mutate(&spec)
			if err := spec.Validate(tt.market, now, 5*time.Second); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestOrderSpecRejectsInvalidMatrixCombinations(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	price := shared.MustDecimal("100")
	valid := OrderSpec{
		ClientOrderSpec: ClientOrderSpec{
			ExchangeAccountID: "account-1", ClientOrderID: "client-1",
			InstrumentID: "BTC-USDT", Type: exchange.OrderTypeLimit,
			FillPolicy: exchange.FillPolicyGTC, Side: exchange.SideBuy,
			Quantity: shared.MustDecimal("1"), LimitPrice: &price,
		},
		ReferencePrice:   price,
		ReferencePriceAt: now.Add(-time.Second),
		Owner:            OrderOwner{Type: "RPC"},
	}

	tests := []struct {
		name   string
		market exchange.MarketType
		mutate func(*OrderSpec)
	}{
		{"zero quantity", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.Quantity = shared.Zero() }},
		{"negative quantity", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.Quantity = shared.MustDecimal("-1") }},
		{"SPOT position side", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.PositionSide = exchange.PositionSideNet }},
		{"SPOT reduce-only", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.ReducePositionOnly = true }},
		{"MARKET limit price", exchange.MarketTypeSpot, func(spec *OrderSpec) {
			spec.Type = exchange.OrderTypeMarket
			spec.FillPolicy = exchange.FillPolicyUnspecified
		}},
		{"MARKET time in force", exchange.MarketTypeSpot, func(spec *OrderSpec) {
			spec.Type = exchange.OrderTypeMarket
			spec.LimitPrice = nil
		}},
		{"LIMIT missing price", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.LimitPrice = nil }},
		{"LIMIT missing time in force", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.FillPolicy = exchange.FillPolicyUnspecified }},
		{"LIMIT unsupported time in force", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.FillPolicy = exchange.FillPolicy("POST_ONLY") }},
		{"SWAP missing NET", exchange.MarketTypeSwap, func(spec *OrderSpec) {}},
		{"stale reference price", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.ReferencePriceAt = now.Add(-6 * time.Second) }},
		{"zero reference price", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.ReferencePrice = shared.Zero() }},
		{"unsupported market", exchange.MarketType("MARGIN"), func(spec *OrderSpec) {}},
		{"unsupported order type", exchange.MarketTypeSpot, func(spec *OrderSpec) { spec.Type = exchange.OrderType("STOP") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := valid
			tt.mutate(&spec)
			err := spec.Validate(tt.market, now, 5*time.Second)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate() error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestOrderSpecHasNoQuoteAmount(t *testing.T) {
	specType := reflect.TypeOf(OrderSpec{})
	for i := 0; i < specType.NumField(); i++ {
		if strings.Contains(strings.ToLower(specType.Field(i).Name), "quote") {
			t.Fatalf("OrderSpec field %q permits quote-denominated orders", specType.Field(i).Name)
		}
	}
}

func TestClientOrderSpecUsesFillPolicyWithoutReduceOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	price := shared.MustDecimal("100")
	spec := OrderSpec{
		ClientOrderSpec: ClientOrderSpec{
			ExchangeAccountID: "account-1",
			ClientOrderID:     "client-1",
			InstrumentID:      "BTC-USDT",
			Side:              exchange.SideBuy,
			Type:              exchange.OrderTypeLimit,
			FillPolicy:        exchange.FillPolicyIOC,
			Quantity:          shared.MustDecimal("1"),
			LimitPrice:        &price,
		},
		ReferencePrice: price, ReferencePriceAt: now,
		Owner: OrderOwner{Type: "OPERATOR"},
	}

	if err := spec.Validate(exchange.MarketTypeSpot, now, time.Second); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if spec.FillPolicy != exchange.FillPolicyIOC || spec.ReducePositionOnly {
		t.Fatalf("spec = %+v", spec)
	}
}
