package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SelectEnabledMembers resolves a recalc selection against a set's members:
// no keys means every enabled member, explicit keys must all be enabled members.
func SelectEnabledMembers(members []TaskMember, keys []string) ([]MemberDef, error) {
	byKey := make(map[string]TaskMember, len(members))
	enabled := make([]MemberDef, 0, len(members))
	for _, member := range members {
		byKey[member.MemberKey] = member
		if member.Status == MemberStatusEnabled {
			enabled = append(enabled, member.Def())
		}
	}
	if len(keys) == 0 {
		if len(enabled) == 0 {
			return nil, errors.New("factor task has no enabled members")
		}
		return enabled, nil
	}
	requested := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("member keys contain an empty value")
		}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			requested = append(requested, key)
		}
	}
	sort.Strings(requested)
	selected := make([]MemberDef, 0, len(requested))
	for _, key := range requested {
		member, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("成员 %q 不在因子任务里", key)
		}
		if member.Status != MemberStatusEnabled {
			return nil, fmt.Errorf("member %q is not enabled in set", key)
		}
		selected = append(selected, member.Def())
	}
	return selected, nil
}
