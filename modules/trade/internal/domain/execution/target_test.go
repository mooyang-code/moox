package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

func TestTargetIntentValidation(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	valid := TargetIntent{
		ExecutionID:        "execution-1",
		StrategyRunID:      "run-1",
		ExecutionBindingID: "binding-1",
		ExchangeAccountID:  "account-1",
		CommandSequence:    1,
		NotAfter:           now.Add(time.Minute),
		DataRevision:       "revision-1",
		Targets: []Target{
			{InstrumentID: "instrument-1", Symbol: "BTC-USDT", TargetQuantity: shared.MustDecimal("-0.25")},
		},
	}
	if err := valid.Validate(now); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*TargetIntent)
	}{
		{"missing identity", func(intent *TargetIntent) { intent.ExecutionBindingID = "" }},
		{"zero sequence", func(intent *TargetIntent) { intent.CommandSequence = 0 }},
		{"expired", func(intent *TargetIntent) { intent.NotAfter = now }},
		{"missing target", func(intent *TargetIntent) { intent.Targets = nil }},
		{"blank target identity", func(intent *TargetIntent) { intent.Targets[0].Symbol = "" }},
		{"duplicate symbol", func(intent *TargetIntent) { intent.Targets = append(intent.Targets, intent.Targets[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := valid
			intent.Targets = append([]Target(nil), valid.Targets...)
			tt.mutate(&intent)
			if !errors.Is(intent.Validate(now), ErrInvalidTarget) {
				t.Fatalf("Validate() error = %v", intent.Validate(now))
			}
		})
	}
}
