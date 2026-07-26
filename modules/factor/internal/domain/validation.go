package domain

import (
	"fmt"
	"sort"
	"strings"
)

// NormalizeFactorDefinition validates and canonicalizes a factor definition.
func NormalizeFactorDefinition(factor FactorDef) (FactorDef, error) {
	factor.FactorID = strings.TrimSpace(factor.FactorID)
	factor.Name = strings.TrimSpace(factor.Name)
	factor.SourceCode = strings.TrimSpace(factor.SourceCode)
	factor.SourceHash = strings.TrimSpace(factor.SourceHash)
	factor.SourcePath = strings.TrimSpace(factor.SourcePath)
	factor.Status = strings.TrimSpace(factor.Status)
	if factor.FactorID == "" || factor.Name == "" || factor.SourceCode == "" {
		return FactorDef{}, fmt.Errorf("factor_id, name and source_code are required")
	}
	if factor.Status == "" {
		factor.Status = FactorStatusDisabled
	}
	if factor.Status != FactorStatusEnabled && factor.Status != FactorStatusDisabled {
		return FactorDef{}, fmt.Errorf("invalid factor status %q", factor.Status)
	}
	if len(factor.Periods) == 0 {
		return FactorDef{}, fmt.Errorf("periods are required")
	}
	periodSet := make(map[int]struct{}, len(factor.Periods))
	periods := make([]int, 0, len(factor.Periods))
	for _, period := range factor.Periods {
		if period <= 0 {
			return FactorDef{}, fmt.Errorf("period must be positive: %d", period)
		}
		if _, ok := periodSet[period]; ok {
			continue
		}
		periodSet[period] = struct{}{}
		periods = append(periods, period)
	}
	sort.Ints(periods)
	if factor.LookbackBars < periods[len(periods)-1] {
		return FactorDef{}, fmt.Errorf("lookback_bars %d is smaller than maximum period %d", factor.LookbackBars, periods[len(periods)-1])
	}
	factor.Periods = periods
	dependsSet := make(map[string]struct{}, len(factor.Depends))
	depends := make([]string, 0, len(factor.Depends))
	for _, dependency := range factor.Depends {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			continue
		}
		if _, ok := dependsSet[dependency]; ok {
			continue
		}
		dependsSet[dependency] = struct{}{}
		depends = append(depends, dependency)
	}
	sort.Strings(depends)
	factor.Depends = depends
	return factor, nil
}
