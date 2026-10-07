package input

import (
	"sort"
)

// ReadinessChecker performs the stateless strict completeness check for one
// evaluation period. It deliberately holds no cross-period state; a later
// ready event simply loads the same period again.
type ReadinessChecker struct{}

// CheckWithPresenceByInstrument is the rule-scoped variant used when different
// rules intentionally bind different factors to different pools. A row only
// needs the factors required by rules that can actually select that row.
func (ReadinessChecker) CheckWithPresenceByInstrument(pool PoolResult, values map[string]InstrumentInput, present map[string]bool, requiredByInstrument map[string][]string) error {
	missing := make([]string, 0)
	for _, item := range pool.Items {
		if present != nil && !present[item.InstrumentID] {
			missing = append(missing, item.InstrumentID+":source_row")
			continue
		}
		row, ok := values[item.InstrumentID]
		if !ok {
			missing = append(missing, item.InstrumentID)
			continue
		}
		for _, factorID := range requiredByInstrument[item.InstrumentID] {
			if _, present := row.Values[factorID]; !present {
				missing = append(missing, item.InstrumentID+":"+factorID)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &StrictIncompleteError{Pool: pool, Missing: missing}
	}
	return nil
}
