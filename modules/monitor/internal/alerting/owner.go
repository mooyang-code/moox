package alerting

import (
	"hash/fnv"
	"sort"
)

func Owner(checkID, ruleID string, activeInstanceIDs []string) string {
	if len(activeInstanceIDs) == 0 {
		return ""
	}
	ids := append([]string(nil), activeInstanceIDs...)
	sort.Strings(ids)
	h := fnv.New32a()
	_, _ = h.Write([]byte(checkID + ":" + ruleID))
	return ids[int(h.Sum32())%len(ids)]
}
