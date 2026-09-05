package input

import (
	"sort"
)

// ReadinessChecker performs the stateless strict completeness check for one
// evaluation period. It deliberately holds no cross-period state; a later
// ready event simply loads the same period again.
type ReadinessChecker struct{}

func (ReadinessChecker) Check(pool PoolResult, values map[string]InstrumentInput, requiredFactors []string) error {
	return (ReadinessChecker{}).CheckWithPresence(pool, values, nil, requiredFactors)
}

// CheckWithPresence additionally verifies that the source View contained a
// current-period row for every admitted instrument.  Keeping this separate
// from Check preserves the small, map-based contract used by unit callers
// while allowing the RPC loader to distinguish a missing source row from a
// row that merely has no factor value yet.
func (ReadinessChecker) CheckWithPresence(pool PoolResult, values map[string]InstrumentInput, present map[string]bool, requiredFactors []string) error {
	requiredByInstrument := make(map[string][]string, len(pool.Items))
	for _, item := range pool.Items {
		requiredByInstrument[item.InstrumentID] = append([]string(nil), requiredFactors...)
	}
	return (ReadinessChecker{}).CheckWithPresenceByInstrument(pool, values, present, requiredByInstrument)
}

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
