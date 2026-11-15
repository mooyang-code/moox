package pipeline

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// 管线一律按序列（标的、series_tag）判断期望全集、缺失、跳过与失败：只有一个 series_tag 的数据集每个标的一条序列，
// 统一数据集（同一个数据集里同时放现货、永续两个 series_tag）每个标的至多两条。

// uniqueSeriesSorted 去掉空项与重复项，按（标的、series_tag）排序。
func uniqueSeriesSorted(values []seriesset.Series) []seriesset.Series {
	seen := make(map[seriesset.Series]struct{}, len(values))
	out := make([]seriesset.Series, 0, len(values))
	for _, value := range values {
		value.SubjectID, value.SeriesTag = strings.TrimSpace(value.SubjectID), strings.TrimSpace(value.SeriesTag)
		if value.SubjectID == "" || value.SeriesTag == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return seriesLess(out[i], out[j]) })
	return out
}

func seriesLess(a, b seriesset.Series) bool {
	if a.SubjectID != b.SubjectID {
		return a.SubjectID < b.SubjectID
	}
	return a.SeriesTag < b.SeriesTag
}

// seriesSubjects 返回序列涉及的标的（排序、去重）。
func seriesSubjects(values []seriesset.Series) []string {
	subjects := make([]string, 0, len(values))
	for _, value := range values {
		subjects = append(subjects, value.SubjectID)
	}
	return uniqueSorted(subjects)
}

// seriesTags 返回序列涉及的 series_tag（排序、去重）。
func seriesTags(values []seriesset.Series) []string {
	tags := make([]string, 0, 2)
	for _, value := range values {
		tags = append(tags, value.SeriesTag)
	}
	return uniqueSorted(tags)
}

// seriesSet 把序列列表做成集合。
func seriesSet(values []seriesset.Series) map[seriesset.Series]struct{} {
	set := make(map[seriesset.Series]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// intersectSeries 返回 left 中也在 right 里的序列（保持 left 的顺序）。
func intersectSeries(left, right []seriesset.Series) []seriesset.Series {
	rightSet := seriesSet(right)
	out := make([]seriesset.Series, 0, len(left))
	for _, value := range left {
		if _, ok := rightSet[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

// differenceSeries 返回 left 中不在 right 里的序列（保持 left 的顺序）。
func differenceSeries(left, right []seriesset.Series) []seriesset.Series {
	rightSet := seriesSet(right)
	out := make([]seriesset.Series, 0, len(left))
	for _, value := range left {
		if _, ok := rightSet[value]; !ok {
			out = append(out, value)
		}
	}
	return out
}

// seriesOfSubjects 返回 values 中属于这些标的的序列。
func seriesOfSubjects(values []seriesset.Series, subjects []string) []seriesset.Series {
	set := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		set[subject] = struct{}{}
	}
	out := make([]seriesset.Series, 0, len(values))
	for _, value := range values {
		if _, ok := set[value.SubjectID]; ok {
			out = append(out, value)
		}
	}
	return out
}

// tagsBySubject 把序列按标的归组：标的 → 排好序的 series_tag。
func tagsBySubject(values []seriesset.Series) map[string][]string {
	out := make(map[string][]string)
	for _, value := range values {
		out[value.SubjectID] = append(out[value.SubjectID], value.SeriesTag)
	}
	for subject := range out {
		sort.Strings(out[subject])
	}
	return out
}

// TagsBySubject 返回计划里每个标的期望的 series_tag（标的 → tag）。数据层据此按各序列自己的进度同步与判断。
func (p Plan) TagsBySubject() map[string][]string { return tagsBySubject(p.ExpectedSeries) }

// SubjectSeries 把按标的选定的范围换成序列：每个标的配上视图里它的各个 series_tag。给手里只有标的名单和已读入数据的调用方
// （试算、回放）用，结果与“按标的判断”相同：标的缺数就是它的序列缺数。视图里没有数据的标的配上这些标的里最小的那个 tag
// （它必然缺数，配哪个 tag 只影响记在哪个 tag 名下）；视图里一个 tag 都没有时这些标的没有序列。
func SubjectSeries(view *serieswindow.View, subjects []string) []seriesset.Series {
	var out []seriesset.Series
	var empty []string
	fallback := ""
	for _, subject := range subjects {
		list := view.Series(subject)
		if len(list) == 0 {
			empty = append(empty, subject)
			continue
		}
		for _, sv := range list {
			out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: sv.Tag})
			if fallback == "" || sv.Tag < fallback {
				fallback = sv.Tag
			}
		}
	}
	if fallback != "" {
		for _, subject := range empty {
			out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: fallback})
		}
	}
	return uniqueSeriesSorted(out)
}
