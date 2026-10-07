package domain

import (
	"testing"
)

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
