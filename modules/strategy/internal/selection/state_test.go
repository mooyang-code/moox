package selection

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestCloneRuleStateDoesNotAliasBatchWeights(t *testing.T) {
	original := domain.RuleState{Batches: []domain.HoldingBatchState{{Offset: 0, BaseWeights: map[string]string{"A": "0.5"}}}}
	clone := CloneRuleState(original)
	clone.Batches[0].BaseWeights["A"] = "0.25"
	if original.Batches[0].BaseWeights["A"] != "0.5" {
		t.Fatalf("clone mutated original: %#v", original)
	}
}

func TestNormalizeRuleStateOrdersPersistedState(t *testing.T) {
	state := domain.RuleState{
		Signals: []domain.SignalState{{InstrumentID: "B"}, {InstrumentID: "A"}},
		Batches: []domain.HoldingBatchState{{Offset: 12}, {Offset: 0}},
	}
	got := NormalizeRuleState(state)
	if got.Signals[0].InstrumentID != "A" || got.Batches[0].Offset != 0 {
		t.Fatalf("normalized state = %#v", got)
	}
}
