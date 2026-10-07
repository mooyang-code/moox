package input

import (
	"fmt"
)

func (in EvaluationInput) Validate() error {
	seen := make(map[string]struct{}, len(in.Items))
	for i, item := range in.Items {
		if item.InstrumentID == "" {
			return fmt.Errorf("evaluation input item %d instrument_id is required", i)
		}
		if _, exists := seen[item.InstrumentID]; exists {
			return fmt.Errorf("evaluation input instrument %q is duplicated", item.InstrumentID)
		}
		seen[item.InstrumentID] = struct{}{}
		for factorID := range item.Values {
			if factorID == "" {
				return fmt.Errorf("evaluation input item %d factor_id is required", i)
			}
		}
	}
	return nil
}
