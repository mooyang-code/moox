package factorname

import (
	"sort"
	"strings"
)

// MemberFailedSubjects 还原因子周期明细里一个成员完整的失败标的：周期级 failed_subjects ∪ 明细里该成员的 failed_subjects
// （明细里成员只记周期级之外多出来的部分），返回去重、排序后的并集，去掉空项。
// 读取 PeriodDetail 的地方（因子自己的校验、Strategy 的就绪判断）一律用它取成员的失败标的。
func MemberFailedSubjects(periodFailed, memberFailed []string) []string {
	seen := make(map[string]struct{}, len(periodFailed)+len(memberFailed))
	out := make([]string, 0, len(periodFailed)+len(memberFailed))
	for _, group := range [][]string{periodFailed, memberFailed} {
		for _, subject := range group {
			subject = strings.TrimSpace(subject)
			if subject == "" {
				continue
			}
			if _, ok := seen[subject]; ok {
				continue
			}
			seen[subject] = struct{}{}
			out = append(out, subject)
		}
	}
	sort.Strings(out)
	return out
}
