package seriesset

import (
	"sort"
	"strings"
)

// Union 合并若干份序列名单：去掉空项（标的或 series_tag 为空）与重复项，按（标的、series_tag）排序；没有序列时返回 nil。
//
// 因子周期明细（PeriodDetail）里成员的失败序列就是这样还原的：成员完整的失败序列 =
// 周期级共享的 shared_failed_series ∪ 该成员自己的 failed_series，即 Union(shared, own)。
// 策略、因子等读 FactorPeriodState.failed_series 的地方都要先这样合并，不能直接读成员级名单。
func Union(lists ...[]Series) []Series {
	seen := make(map[Series]struct{})
	var out []Series
	for _, list := range lists {
		for _, value := range list {
			value.SubjectID, value.SeriesTag = strings.TrimSpace(value.SubjectID), strings.TrimSpace(value.SeriesTag)
			if value.SubjectID == "" || value.SeriesTag == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectID != out[j].SubjectID {
			return out[i].SubjectID < out[j].SubjectID
		}
		return out[i].SeriesTag < out[j].SeriesTag
	})
	return out
}
