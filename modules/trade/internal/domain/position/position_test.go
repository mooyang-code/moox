package position

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestPositionAcceptsSignedNETQuantity(t *testing.T) {
	base := Position{
		ExchangeAccountID: "account-1",
		Symbol:            "BTC-USDT",
		PositionSide:      exchange.PositionSideNet,
		SignedQuantity:    shared.MustDecimal("1.25"),
		EntryPrice:        shared.MustDecimal("60000"),
		MarkPrice:         shared.MustDecimal("61000"),
		Leverage:          shared.MustDecimal("5"),
		MarginMode:        exchange.MarginModeCross,
		UsedMargin:        shared.MustDecimal("15250"),
		LiquidationPrice:  shared.MustDecimal("48000"),
		UnrealizedPnL:     shared.MustDecimal("1250"),
		RealizedPnL:       shared.Zero(),
	}
	for _, quantity := range []shared.Decimal{
		shared.MustDecimal("1.25"),
		shared.MustDecimal("-1.25"),
		shared.Zero(),
	} {
		position := base
		position.SignedQuantity = quantity
		if err := position.Validate(); err != nil {
			t.Fatalf("quantity %s: Validate() error = %v", quantity.String(), err)
		}
	}
}

func TestPositionRejectsInvalidSnapshot(t *testing.T) {
	base := Position{
		ExchangeAccountID: "account-1",
		Symbol:            "BTC-USDT",
		PositionSide:      exchange.PositionSideNet,
		Leverage:          shared.MustDecimal("5"),
		MarginMode:        exchange.MarginModeCross,
	}
	tests := []struct {
		name   string
		mutate func(*Position)
	}{
		{"missing account", func(position *Position) { position.ExchangeAccountID = "" }},
		{"missing symbol", func(position *Position) { position.Symbol = "" }},
		{"non-NET position", func(position *Position) { position.PositionSide = exchange.PositionSideUnspecified }},
		{"nonpositive leverage", func(position *Position) { position.Leverage = shared.Zero() }},
		{"non-CROSS margin", func(position *Position) { position.MarginMode = exchange.MarginModeUnspecified }},
		{"negative used margin", func(position *Position) { position.UsedMargin = shared.MustDecimal("-1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			position := base
			tt.mutate(&position)
			if !errors.Is(position.Validate(), ErrInvalidPosition) {
				t.Fatalf("Validate() error = %v", position.Validate())
			}
		})
	}
}
