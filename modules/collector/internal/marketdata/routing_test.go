package marketdata

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRendezvousAssignReturnsAllGroupsAndIsStable(t *testing.T) {
	subjects := []string{"600000.XSHG", "000001.XSHE", "601318.XSHG", "300750.XSHE", "000858.XSHE"}

	first, err := RendezvousAssign(subjects, 7, "v1")
	require.NoError(t, err)
	second, err := RendezvousAssign(subjects, 7, "v1")
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Len(t, first, 7)

	seen := make(map[string]int, len(subjects))
	for groupID, group := range first {
		require.Less(t, groupID, 7)
		for _, subjectID := range group {
			seen[subjectID]++
		}
	}

	require.Len(t, seen, len(subjects))
	for _, subjectID := range subjects {
		assert.Equal(t, 1, seen[subjectID])
	}
}

func TestRendezvousAssignRejectsInvalidGroupCount(t *testing.T) {
	_, err := RendezvousAssign([]string{"600000.XSHG"}, 0, "v1")
	require.Error(t, err)
}

func TestAssignProviderChainsBalancesPrimariesAndSpreadsBackups(t *testing.T) {
	weights := map[string]int{
		"alpha": 1,
		"beta":  1,
		"gamma": 1,
	}

	first, err := AssignProviderChains(8, weights, "v1", "2026-08-29")
	require.NoError(t, err)
	second, err := AssignProviderChains(8, weights, "v1", "2026-08-29")
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Len(t, first, 8)

	primaryCounts := map[string]int{}
	seenSecond := map[string]struct{}{}
	for _, chain := range first {
		require.Len(t, chain, len(weights))
		primaryCounts[chain[0]]++
		seenSecond[chain[1]] = struct{}{}

		unique := map[string]struct{}{}
		for _, provider := range chain {
			unique[provider] = struct{}{}
		}
		require.Len(t, unique, len(weights))
	}

	minCount := 1 << 30
	maxCount := 0
	for _, count := range primaryCounts {
		if count < minCount {
			minCount = count
		}
		if count > maxCount {
			maxCount = count
		}
	}
	require.LessOrEqual(t, maxCount-minCount, 1)
	require.Greater(t, len(seenSecond), 1)
}

func TestAssignProviderChainsUsesExactEqualWeightDistribution(t *testing.T) {
	three, err := AssignProviderChains(200, map[string]int{"eastmoney": 1, "sina": 1, "tencent": 1}, "stockcn_equity_kline_1m_v4", "2026-08-29")
	require.NoError(t, err)
	threeCounts := map[string]int{}
	for _, chain := range three {
		threeCounts[chain[0]]++
	}
	assert.Equal(t, []int{66, 67, 67}, sortedCounts(threeCounts))

	four, err := AssignProviderChains(200, map[string]int{"baidu": 1, "eastmoney": 1, "sina": 1, "tencent": 1}, "stockcn_equity_kline_1m_v4", "2026-08-29")
	require.NoError(t, err)
	fourCounts := map[string]int{}
	for _, chain := range four {
		fourCounts[chain[0]]++
	}
	assert.Equal(t, map[string]int{"baidu": 50, "eastmoney": 50, "sina": 50, "tencent": 50}, fourCounts)
}

func sortedCounts(counts map[string]int) []int {
	values := make([]int, 0, len(counts))
	for _, count := range counts {
		values = append(values, count)
	}
	sort.Ints(values)
	return values
}

func TestAssignProviderChainsRejectsInvalidInputs(t *testing.T) {
	_, err := AssignProviderChains(0, map[string]int{"alpha": 1, "beta": 1}, "v1", "2026-08-29")
	require.Error(t, err)

	_, err = AssignProviderChains(1, map[string]int{"alpha": 1}, "v1", "2026-08-29")
	require.Error(t, err)

	_, err = AssignProviderChains(1, map[string]int{"alpha": 1, "beta": 0}, "v1", "2026-08-29")
	require.Error(t, err)
}
