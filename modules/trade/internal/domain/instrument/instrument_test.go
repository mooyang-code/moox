package instrument

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestInstrumentValidation(t *testing.T) {
	spot := Instrument{
		Exchange:             exchange.ExchangeBinance,
		MarketType:           exchange.MarketTypeSpot,
		Symbol:               "BTC-USDT",
		BaseAsset:            "BTC",
		QuoteAsset:           "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.00001"),
		MinExchangeQuantity:  shared.MustDecimal("0.0001"),
		PriceTick:            shared.MustDecimal("0.01"),
		MinNotional:          shared.MustDecimal("5"),
		Status:               StatusEnabled,
	}
	swap := spot
	swap.Exchange = exchange.ExchangeOKX
	swap.MarketType = exchange.MarketTypeSwap
	swap.SettlementAsset = "USDT"
	swap.Linear = true
	swap.ContractValue = shared.MustDecimal("0.01")
	swap.ContractValueAsset = "BTC"
	swap.ExchangeQuantityStep = shared.MustDecimal("1")
	swap.MinExchangeQuantity = shared.MustDecimal("1")

	tests := []struct {
		name       string
		instrument Instrument
		wantErr    bool
	}{
		{name: "valid SPOT", instrument: spot},
		{name: "valid linear SWAP", instrument: swap},
		{
			name: "SWAP must be linear",
			instrument: mutateInstrument(swap, func(value *Instrument) {
				value.Linear = false
			}),
			wantErr: true,
		},
		{
			name: "SWAP contract value must use base asset",
			instrument: mutateInstrument(swap, func(value *Instrument) {
				value.ContractValueAsset = "USDT"
			}),
			wantErr: true,
		},
		{
			name: "zero quantity step",
			instrument: mutateInstrument(spot, func(value *Instrument) {
				value.ExchangeQuantityStep = shared.Zero()
			}),
			wantErr: true,
		},
		{
			name: "negative minimum",
			instrument: mutateInstrument(spot, func(value *Instrument) {
				value.MinNotional = shared.MustDecimal("-1")
			}),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.instrument.Validate()
			if tt.wantErr && !errors.Is(err, ErrInvalidInstrument) {
				t.Fatalf("Validate() error = %v, want ErrInvalidInstrument", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func mutateInstrument(value Instrument, fn func(*Instrument)) Instrument {
	fn(&value)
	return value
}
