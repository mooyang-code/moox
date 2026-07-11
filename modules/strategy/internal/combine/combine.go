package combine

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"math/big"
)

func Combine(members map[string][]domain.TargetWeight, weights map[string]string) []domain.TargetWeight {
	tot := map[string]*big.Rat{}
	for id, targets := range members {
		w := new(big.Rat)
		if _, ok := w.SetString(weights[id]); !ok {
			continue
		}
		for _, t := range targets {
			v := new(big.Rat)
			if _, ok := v.SetString(t.TargetWeight); !ok {
				continue
			}
			v.Mul(v, w)
			if tot[t.InstrumentID] == nil {
				tot[t.InstrumentID] = new(big.Rat)
			}
			tot[t.InstrumentID].Add(tot[t.InstrumentID], v)
		}
	}
	out := make([]domain.TargetWeight, 0, len(tot))
	for id, v := range tot {
		out = append(out, domain.TargetWeight{InstrumentID: id, TargetWeight: v.FloatString(12)})
	}
	return out
}
