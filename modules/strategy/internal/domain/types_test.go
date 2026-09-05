package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStrategyPersistenceTypesUseFinalTables(t *testing.T) {
	logicalAccountID := "logical-account-1"
	lastResultID := "result-1"
	lastSuccessAt := time.UnixMilli(1000).UTC()
	lastError := "storage not ready"
	sequence := int64(7)

	strategy := Strategy{ID: "strategy-1", Name: "trend"}
	runner := StrategyRunner{
		ID:                 "runner-1",
		StrategyID:         strategy.ID,
		LogicalAccountID:   &logicalAccountID,
		Status:             RunnerStatusEnabled,
		CurrentTargetsJSON: json.RawMessage(`[]`),
		CommandSequence:    sequence,
		LastResultID:       &lastResultID,
		LastSuccessAt:      &lastSuccessAt,
		LastError:          &lastError,
	}
	result := StrategyResult{
		ID:              lastResultID,
		RunnerID:        runner.ID,
		StrategyID:      strategy.ID,
		PeriodTime:      time.UnixMilli(2000).UTC(),
		TargetsJSON:     json.RawMessage(`[]`),
		DebugInfoJSON:   json.RawMessage(`{}`),
		InputHash:       "input-hash",
		Action:          ActionRebalance,
		CommandSequence: &sequence,
	}

	if got := strategy.TableName(); got != "t_strategies" {
		t.Fatalf("Strategy.TableName() = %q", got)
	}
	if got := runner.TableName(); got != "t_strategy_runners" {
		t.Fatalf("StrategyRunner.TableName() = %q", got)
	}
	if got := result.TableName(); got != "t_strategy_results" {
		t.Fatalf("StrategyResult.TableName() = %q", got)
	}
}

func TestRuleStateContainsOnlyTheoreticalRuleData(t *testing.T) {
	state := RuleState{
		Signals: []SignalState{{InstrumentID: "BTC-USDT-SPOT", EnteredAt: 1000}},
		Batches: []HoldingBatchState{{
			Offset: 12, EstablishedAt: 1000, ExpiresAt: 2000,
			BaseWeights: map[string]string{"BTC-USDT-SPOT": "0.5"},
		}},
	}
	if len(state.Signals) != 1 || state.Signals[0].InstrumentID != "BTC-USDT-SPOT" {
		t.Fatalf("signal state = %+v", state.Signals)
	}
	if len(state.Batches) != 1 || state.Batches[0].BaseWeights["BTC-USDT-SPOT"] != "0.5" {
		t.Fatalf("batch state = %+v", state.Batches)
	}
	if state.Batches[0].EstablishedAt >= state.Batches[0].ExpiresAt {
		t.Fatalf("invalid batch interval = %+v", state.Batches[0])
	}
}
