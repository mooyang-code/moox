package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SelectEnabledFactors resolves a recalc selection against a set's members:
// no ids means every enabled member, explicit ids must all be enabled members.
func SelectEnabledFactors(members []SetMember, factorIDs []string) ([]FactorDef, error) {
	byID := make(map[string]SetMember, len(members))
	enabled := make([]FactorDef, 0, len(members))
	for _, member := range members {
		byID[member.FactorID] = member
		if member.Status == MemberStatusEnabled {
			enabled = append(enabled, member.Factor)
		}
	}
	if len(factorIDs) == 0 {
		if len(enabled) == 0 {
			return nil, errors.New("factor set has no enabled members")
		}
		return enabled, nil
	}
	requested := make([]string, 0, len(factorIDs))
	seen := make(map[string]struct{}, len(factorIDs))
	for _, id := range factorIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("factor_ids contains an empty value")
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			requested = append(requested, id)
		}
	}
	sort.Strings(requested)
	selected := make([]FactorDef, 0, len(requested))
	for _, id := range requested {
		member, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("factor %q is not a member of set", id)
		}
		if member.Status != MemberStatusEnabled {
			return nil, fmt.Errorf("factor %q is not enabled in set", id)
		}
		selected = append(selected, member.Factor)
	}
	return selected, nil
}
