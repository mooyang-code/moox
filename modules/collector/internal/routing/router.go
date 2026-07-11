package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type Candidate struct {
	ProviderID   marketdata.ProviderID
	Weight       float64
	Enabled      bool
	Capabilities []providers.Capability
	Health       HealthState
}

type RouteRequest struct {
	ShardKey   string
	Query      providers.CapabilityQuery
	Candidates []Candidate
}

// Route returns a deterministic candidate chain. Capability and health gates
// are applied before weighted rendezvous scoring, so disabled/open providers
// can never win merely because they have a larger weight.
func Route(request RouteRequest) ([]marketdata.ProviderID, error) {
	if request.ShardKey == "" {
		return nil, fmt.Errorf("shard key is required")
	}
	type scored struct {
		id    marketdata.ProviderID
		score float64
	}
	scores := make([]scored, 0, len(request.Candidates))
	seen := make(map[marketdata.ProviderID]bool, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.ProviderID == "" || seen[candidate.ProviderID] {
			return nil, fmt.Errorf("candidate provider ids must be unique and non-empty")
		}
		seen[candidate.ProviderID] = true
		if !candidate.Enabled || candidate.Weight <= 0 || candidate.Health == HealthOpen || !matches(candidate.Capabilities, request.Query) {
			continue
		}
		u := hashUnit(request.ShardKey + "\x00" + string(candidate.ProviderID))
		scores = append(scores, scored{id: candidate.ProviderID, score: candidate.Weight / -math.Log(u)})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score == scores[j].score {
			return scores[i].id < scores[j].id
		}
		return scores[i].score > scores[j].score
	})
	result := make([]marketdata.ProviderID, len(scores))
	for index := range scores {
		result[index] = scores[index].id
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no ready provider matches requested capability")
	}
	return result, nil
}

func matches(capabilities []providers.Capability, query providers.CapabilityQuery) bool {
	for _, capability := range capabilities {
		if capability.Matches(query) {
			return true
		}
	}
	return false
}

func hashUnit(value string) float64 {
	sum := sha256.Sum256([]byte(value))
	n := binary.BigEndian.Uint64(sum[:8])
	// Keep the value in (0, 1), including for all-zero/all-one prefixes.
	return (float64(n) + 1) / (float64(math.MaxUint64) + 2)
}
