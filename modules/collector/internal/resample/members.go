package resample

import (
	"sort"

	"github.com/mooyang-code/moox/packages/seriesset"
)

// Members 是合成任务各条源序列的成员：series_tag 到标的（去重、升序）。
// 单序列任务只有一项；统一数据集（现货加永续）每个市场一项，各取自己标签的成员，不取并集。
type Members map[string][]string

// Union 返回全部序列里出现过的标的（去重、升序）。
func (m Members) Union() []string {
	var all []string
	for _, subjects := range m {
		all = append(all, subjects...)
	}
	return uniqueSorted(all)
}

// Series 返回全部序列（标的、series_tag）。
func (m Members) Series() []seriesset.Series {
	tags := make([]string, 0, len(m))
	for tag := range m {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	var out []seriesset.Series
	for _, tag := range tags {
		for _, subject := range m[tag] {
			out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: tag})
		}
	}
	return out
}

// Count 返回序列总数。
func (m Members) Count() int {
	total := 0
	for _, subjects := range m {
		total += len(subjects)
	}
	return total
}

// normalized 返回每项去重、升序且不含空项的副本。
func (m Members) normalized() Members {
	out := make(Members, len(m))
	for tag, subjects := range m {
		if cleaned := uniqueSorted(subjects); len(cleaned) > 0 {
			out[tag] = cleaned
		}
	}
	return out
}

func sameMembers(a, b Members) bool {
	a, b = a.normalized(), b.normalized()
	if len(a) != len(b) {
		return false
	}
	for tag, subjects := range a {
		if !sameStrings(subjects, b[tag]) {
			return false
		}
	}
	return true
}

// SingleTagMembers 把一组标的当作任务唯一源序列的成员；任务有多条源序列时每条都取同一组。
func SingleTagMembers(task *Task, subjects []string) Members {
	out := Members{}
	for _, tag := range task.Tags {
		out[tag] = uniqueSorted(subjects)
	}
	return out
}
