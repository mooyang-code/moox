package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestValidateOutput(t *testing.T) {
	if err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.TargetPosition{{InstrumentID: "BTC", Symbol: "BTCUSDT", TargetQuantity: "1"}}, NextState: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOutputRejectsInvalidTargetQuantities(t *testing.T) {
	for _, quantity := range []string{
		"", "+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf", " 1", "1 ",
		strings.Repeat("9", maxTargetQuantityLength+1),
	} {
		t.Run(quantity, func(t *testing.T) {
			output := domain.Output{Action: domain.ActionRebalance, Targets: []domain.TargetPosition{{
				InstrumentID: "BTC", Symbol: "BTCUSDT", TargetQuantity: quantity,
			}}, NextState: map[string]any{}}
			if err := Validate(output); err == nil {
				t.Fatalf("quantity %q must be rejected", quantity)
			}
		})
	}
}

func TestValidateOutputRejectsDuplicateSymbol(t *testing.T) {
	output := domain.Output{Action: domain.ActionRebalance, Targets: []domain.TargetPosition{
		{InstrumentID: "BTC-USDT", Symbol: "BTCUSDT", TargetQuantity: "1"},
		{InstrumentID: "BTC-USDT-SWAP", Symbol: "BTCUSDT", TargetQuantity: "2"},
	}, NextState: map[string]any{}}
	if err := Validate(output); err == nil {
		t.Fatal("duplicate symbol was accepted")
	}
}

func TestValidateOutputRejectsEmptyRebalance(t *testing.T) {
	if err := Validate(domain.Output{
		Action: domain.ActionRebalance, NextState: map[string]any{},
	}); err == nil {
		t.Fatal("empty rebalance was accepted")
	}
}

func TestValidateOutputRejectsLegacyTargetWeight(t *testing.T) {
	var output domain.Output
	if err := json.Unmarshal([]byte(`{
		"action":"rebalance",
		"targets":[{"instrument_id":"BTC-USDT","symbol":"BTCUSDT","target_quantity":"1","target_weight":"0.5"}],
		"next_state":{}
	}`), &output); err == nil {
		t.Fatal("legacy target_weight was decoded")
	}
}

func TestValidateOutputRejectsWhitespaceTargetIdentity(t *testing.T) {
	for _, target := range []domain.TargetPosition{
		{InstrumentID: " ", Symbol: "BTCUSDT", TargetQuantity: "1"},
		{InstrumentID: "BTC-USDT", Symbol: "\t", TargetQuantity: "1"},
	} {
		if err := Validate(domain.Output{
			Action: domain.ActionRebalance, Targets: []domain.TargetPosition{target},
			NextState: map[string]any{},
		}); err == nil {
			t.Fatalf("whitespace identity was accepted: %+v", target)
		}
	}
}
