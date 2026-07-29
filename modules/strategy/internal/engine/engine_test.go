package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
)

func TestValidateOutputAcceptsEmptyRebalance(t *testing.T) {
	if err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.InstrumentTarget{}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsLoadedSourceThatDoesNotMatchPersistedStrategy(t *testing.T) {
	runtime := &Engine{strategies: map[string]process.LoadRequest{
		"strategy-1": {
			LogicalID:  "strategy-1",
			SourceHash: "rejected-source",
		},
	}}
	_, _, err := runtime.Run(
		context.Background(),
		domain.ExecutionRequest{StrategyID: "strategy-1"},
		domain.Strategy{ID: "strategy-1", SourceHash: "persisted-source"},
	)
	if err == nil || !strings.Contains(err.Error(), "source hash") {
		t.Fatalf("Run() error = %v", err)
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
		Namespace: "live", Data: []map[string]any{{"time": "2026-07-29T10:00:00Z", "close": "1"}},
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
		Namespace: "live", Data: []map[string]any{{"time": "2026-07-29T10:00:00Z", "close": "1"}},
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
	variants[2].Data = []map[string]any{{"time": "2026-07-29T10:01:00Z", "close": "1"}}
	variants[3].Namespace = "preview"
	variants[4].Data = []map[string]any{{"time": "2026-07-29T10:00:00Z", "close": "2"}}
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

func TestBuildInputRejectsInvalidHistoryWindowTimes(t *testing.T) {
	base := domain.ExecutionRequest{
		StrategyID: "strategy-1", RunnerID: "runner-1",
		TriggerBarTime: "2026-07-29T10:00:00Z", Namespace: "live",
		Params: map[string]any{},
	}
	tests := map[string][]map[string]any{
		"out of order": {
			{"time": "2026-07-29T09:59:00Z"},
			{"time": "2026-07-29T09:58:00Z"},
			{"time": "2026-07-29T10:00:00Z"},
		},
		"duplicate time": {
			{"time": "2026-07-29T09:59:00Z"},
			{"time": "2026-07-29T09:59:00Z"},
			{"time": "2026-07-29T10:00:00Z"},
		},
		"future final bar": {
			{"time": "2026-07-29T10:00:00Z"},
			{"time": "2026-07-29T10:01:00Z"},
		},
		"stale final bar": {
			{"time": "2026-07-29T09:58:00Z"},
			{"time": "2026-07-29T09:59:00Z"},
		},
		"missing time": {
			{"time": "2026-07-29T09:59:00Z"},
			{"close": "1"},
		},
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			request.Data = data
			if _, _, err := buildInput(request); err == nil {
				t.Fatal("buildInput() accepted an invalid history window")
			}
		})
	}
}

func TestBuildInputAcceptsStrictAscendingHistoryEndingAtTrigger(t *testing.T) {
	_, _, err := buildInput(domain.ExecutionRequest{
		StrategyID: "strategy-1", RunnerID: "runner-1",
		TriggerBarTime: "2026-07-29T10:00:00Z", Namespace: "live",
		Data: []map[string]any{
			{"time": "2026-07-29T09:58:00Z"},
			{"time": "2026-07-29T09:59:00Z"},
			{"time": "2026-07-29T10:00:00Z"},
		},
		Params: map[string]any{},
	})
	if err != nil {
		t.Fatalf("buildInput() error = %v", err)
	}
}

func TestEngineLoadsStrategyByArtifactID(t *testing.T) {
	if got := strategyKey("strategy-1"); got != "strategy-1" {
		t.Fatalf("strategyKey() = %q", got)
	}
}
