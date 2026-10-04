package marketdata

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
)

func RendezvousAssign(subjectIDs []string, n int, routeVersion string) ([][]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be positive")
	}

	groups := make([][]string, n)
	for _, subjectID := range subjectIDs {
		bestGroup := 0
		bestScore := hashScore(routeVersion, subjectID, strconv.Itoa(0))
		for group := 1; group < n; group++ {
			score := hashScore(routeVersion, subjectID, strconv.Itoa(group))
			if score > bestScore {
				bestScore = score
				bestGroup = group
			}
		}
		groups[bestGroup] = append(groups[bestGroup], subjectID)
	}

	for i := range groups {
		sort.Strings(groups[i])
	}
	return groups, nil
}

func AssignProviderChains(n int, providerWeights map[string]int, routeVersion, tradingDate string) ([][]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be positive")
	}
	if len(providerWeights) < 2 {
		return nil, fmt.Errorf("provider count must be at least 2")
	}

	providers := make([]weightedProvider, 0, len(providerWeights))
	for provider, weight := range providerWeights {
		if provider == "" {
			return nil, fmt.Errorf("provider name must be non-empty")
		}
		if weight <= 0 {
			return nil, fmt.Errorf("provider weight for %q must be positive", provider)
		}
		providers = append(providers, weightedProvider{name: provider, weight: weight})
	}
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].name < providers[j].name
	})

	primaries := smoothWeightedRoundRobin(n, providers, routeVersion, tradingDate)
	chains := make([][]string, n)
	for group := 0; group < n; group++ {
		primary := primaries[group]
		chain := make([]string, 0, len(providers))
		chain = append(chain, primary)

		backupOrder := rankedBackups(providers, routeVersion, tradingDate, group, primary)
		chain = append(chain, backupOrder...)
		chains[group] = chain
	}
	return chains, nil
}

type weightedProvider struct {
	name    string
	weight  int
	current int
}

func smoothWeightedRoundRobin(n int, providers []weightedProvider, routeVersion, tradingDate string) []string {
	totalWeight := 0
	for i := range providers {
		totalWeight += providers[i].weight
	}

	primaries := make([]string, n)
	for group := 0; group < n; group++ {
		best := 0
		for i := range providers {
			providers[i].current += providers[i].weight
			if providers[i].current > providers[best].current || (providers[i].current == providers[best].current && hashScore(routeVersion, tradingDate, providers[i].name) > hashScore(routeVersion, tradingDate, providers[best].name)) {
				best = i
			}
		}
		providers[best].current -= totalWeight
		primaries[group] = providers[best].name
	}
	return primaries
}

func rankedBackups(providers []weightedProvider, routeVersion, tradingDate string, group int, primary string) []string {
	type candidate struct {
		name  string
		score uint64
	}

	candidates := make([]candidate, 0, len(providers)-1)
	for _, provider := range providers {
		if provider.name == primary {
			continue
		}
		candidates = append(candidates, candidate{
			name:  provider.name,
			score: hashScore(routeVersion, tradingDate, strconv.Itoa(group), primary, provider.name),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].score > candidates[j].score
	})

	backups := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		backups = append(backups, candidate.name)
	}
	return backups
}

func hashScore(parts ...string) uint64 {
	h := fnv.New64a()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
