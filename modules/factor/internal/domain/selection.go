package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SelectEnabledMembers resolves a recalc selection against a set's members:
// no aliases means every enabled member, explicit aliases must all be enabled members.
func SelectEnabledMembers(members []SetMember, aliases []string) ([]MemberDef, error) {
	byAlias := make(map[string]SetMember, len(members))
	enabled := make([]MemberDef, 0, len(members))
	for _, member := range members {
		byAlias[member.MemberAlias] = member
		if member.Status == MemberStatusEnabled {
			enabled = append(enabled, member.Def())
		}
	}
	if len(aliases) == 0 {
		if len(enabled) == 0 {
			return nil, errors.New("factor set has no enabled members")
		}
		return enabled, nil
	}
	requested := make([]string, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return nil, errors.New("member aliases contain an empty value")
		}
		if _, ok := seen[alias]; !ok {
			seen[alias] = struct{}{}
			requested = append(requested, alias)
		}
	}
	sort.Strings(requested)
	selected := make([]MemberDef, 0, len(requested))
	for _, alias := range requested {
		member, ok := byAlias[alias]
		if !ok {
			return nil, fmt.Errorf("成员 %q 不在因子集里", alias)
		}
		if member.Status != MemberStatusEnabled {
			return nil, fmt.Errorf("member %q is not enabled in set", alias)
		}
		selected = append(selected, member.Def())
	}
	return selected, nil
}
