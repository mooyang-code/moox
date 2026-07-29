package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestValidateOutputAcceptsEmptyRebalance(t *testing.T) {
	if err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOutputRejectsInvalidQuantities(t *testing.T) {
	for _, quantity := range []string{"", "+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf", " 1", "1 ", strings.Repeat("9", maxTargetQuantityLength+1)} {
		t.Run(quantity, func(t *testing.T) {
			err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SPOT", Quantity: quantity,
			}}})
			if err == nil {
				t.Fatalf("quantity %q must be rejected", quantity)
			}
		})
	}
}

func TestValidateOutputRejectsDuplicateInstrument(t *testing.T) {
	err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "2"},
	}})
	if err == nil {
		t.Fatal("duplicate instrument was accepted")
	}
}

func TestValidateOutputRejectsOversizedDebugInfo(t *testing.T) {
	err := Validate(domain.Output{
		Action:    domain.ActionHold,
		DebugInfo: map[string]any{"value": strings.Repeat("x", maxDebugInfoBytes)},
	})
	if err == nil {
		t.Fatal("oversized debug_info was accepted")
	}
}

func TestValidateOutputRejectsNextState(t *testing.T) {
	var output domain.Output
	if err := json.Unmarshal([]byte(`{"action":"hold","targets":[],"next_state":{}}`), &output); err == nil {
		t.Fatal("next_state was decoded")
	}
	if err := json.Unmarshal([]byte(`{"action":"hold","targets":[],"state":{}}`), &output); err == nil {
		t.Fatal("state was decoded")
	}
}

func TestValidateOutputRejectsTargetQuantityAlias(t *testing.T) {
	var output domain.Output
	for _, raw := range []string{
		`{"action":"rebalance","targets":[{"instrument_id":"BTC-USDT-SPOT","target_quantity":"1"}]}`,
		`{"action":"rebalance","targets":[{"instrument_id":"BTC-USDT-SPOT","symbol":"BTCUSDT","quantity":"1"}]}`,
		`{"action":"rebalance","targets":[{"instrument_id":"BTC-USDT-SPOT","native_symbol":"BTCUSDT","quantity":"1"}]}`,
		`{"action":"rebalance","targets":[{"instrument_id":"BTC-USDT-SPOT","account_id":"a","quantity":"1"}]}`,
	} {
		if err := json.Unmarshal([]byte(raw), &output); err == nil {
			t.Fatalf("legacy/ownership field was decoded: %s", raw)
		}
	}
}

func TestRunDoesNotExposePreviousTargetsOrState(t *testing.T) {
	input, _, err := buildInput(domain.ExecutionRequest{
		StrategyID: "strategy-1", RunnerID: "runner-1", TriggerBarTime: "2026-07-29T10:00:00Z",
		Namespace: "live", Data: []map[string]any{{"time": "2026-07-29T09:59:00Z", "close": "1"}},
		Params: map[string]any{"fast": 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["state"]; ok {
		t.Fatal("state was exposed")
	}
	contextValue := got["context"].(map[string]any)
	for _, field := range []string{"state", "state_revision", "previous_targets", "run_time", "data_revision"} {
		if _, ok := contextValue[field]; ok {
			t.Fatalf("context exposed %q", field)
		}
	}
}

func TestRunHashesCompleteHistoryParamsAndTriggerContext(t *testing.T) {
	base := domain.ExecutionRequest{
		StrategyID: "strategy-1", RunnerID: "runner-1", TriggerBarTime: "2026-07-29T10:00:00Z",
		Namespace: "live", Data: []map[string]any{{"time": "2026-07-29T09:59:00Z", "close": "1"}},
		Params: map[string]any{"fast": 12},
	}
	_, first, err := buildInput(base)
	if err != nil {
		t.Fatal(err)
	}
	_, retry, err := buildInput(base)
	if err != nil || retry != first {
		t.Fatalf("stable retry hash = %q, want %q, err=%v", retry, first, err)
	}
	variants := []domain.ExecutionRequest{base, base, base, base, base, base}
	variants[0].StrategyID = "strategy-2"
	variants[1].RunnerID = "runner-2"
	variants[2].TriggerBarTime = "2026-07-29T10:01:00Z"
	variants[3].Namespace = "preview"
	variants[4].Data = []map[string]any{{"time": "2026-07-29T09:59:00Z", "close": "2"}}
	variants[5].Params = map[string]any{"fast": 13}
	for _, variant := range variants {
		_, hash, err := buildInput(variant)
		if err != nil {
			t.Fatal(err)
		}
		if hash == first {
			t.Fatalf("input change did not affect hash: %+v", variant)
		}
	}
}

func TestEngineLoadsStrategyByArtifactID(t *testing.T) {
	if got := strategyKey("strategy-1"); got != "strategy-1" {
		t.Fatalf("strategyKey() = %q", got)
	}
}
