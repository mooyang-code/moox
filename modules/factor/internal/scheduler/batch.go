package scheduler

import (
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"sort"
)

type FactorBatch struct {
	BatchID, ParentTaskID, SnapshotID, SnapshotHash string
	Factors                                         []engine.FactorSpec
	ExpectedColumns                                 []string
	Attempt                                         int
}

func Partition(specs []engine.FactorSpec, cost map[string]int64, maxParallel int, minEstimatedMS int64) []FactorBatch {
	if maxParallel < 1 {
		maxParallel = 1
	}
	total := int64(0)
	for _, s := range specs {
		total += cost[s.FactorID]
	}
	if len(specs) <= 1 || total < minEstimatedMS || maxParallel == 1 {
		return []FactorBatch{{Factors: append([]engine.FactorSpec(nil), specs...)}}
	}
	ordered := append([]engine.FactorSpec(nil), specs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if cost[ordered[i].FactorID] == cost[ordered[j].FactorID] {
			return ordered[i].FactorID < ordered[j].FactorID
		}
		return cost[ordered[i].FactorID] > cost[ordered[j].FactorID]
	})
	batches := make([]FactorBatch, maxParallel)
	loads := make([]int64, maxParallel)
	for _, s := range ordered {
		idx := 0
		for i := 1; i < len(loads); i++ {
			if loads[i] < loads[idx] {
				idx = i
			}
		}
		batches[idx].Factors = append(batches[idx].Factors, s)
		loads[idx] += cost[s.FactorID]
	}
	out := batches[:0]
	for _, b := range batches {
		if len(b.Factors) > 0 {
			out = append(out, b)
		}
	}
	return out
}
