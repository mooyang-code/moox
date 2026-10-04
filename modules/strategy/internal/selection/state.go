package selection

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

// CloneRuleState prevents a failed evaluation from mutating the state supplied
// by the caller. Results are committed only after the whole definition has
// evaluated successfully.
func CloneRuleState(state domain.RuleState) domain.RuleState {
	clone := domain.RuleState{
		Signals: make([]domain.SignalState, len(state.Signals)),
		Batches: make([]domain.HoldingBatchState, len(state.Batches)),
	}
	copy(clone.Signals, state.Signals)
	for i, batch := range state.Batches {
		clone.Batches[i] = batch
		clone.Batches[i].BaseWeights = make(map[string]string, len(batch.BaseWeights))
		for id, weight := range batch.BaseWeights {
			clone.Batches[i].BaseWeights[id] = weight
		}
	}
	return clone
}

// NormalizeRuleState makes persisted state deterministic without changing its
// meaning. It sorts signal IDs and holding batches by their stable offset.
func NormalizeRuleState(state domain.RuleState) domain.RuleState {
	state = CloneRuleState(state)
	sort.SliceStable(state.Signals, func(i, j int) bool {
		return state.Signals[i].InstrumentID < state.Signals[j].InstrumentID
	})
	sort.SliceStable(state.Batches, func(i, j int) bool {
		if state.Batches[i].Offset != state.Batches[j].Offset {
			return state.Batches[i].Offset < state.Batches[j].Offset
		}
		return state.Batches[i].EstablishedAt < state.Batches[j].EstablishedAt
	})
	return state
}
