package domain

import "sort"

// TimeRange 是 Unix 秒的闭区间。
type TimeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// MergeTimeRanges 把闭区间按起点排序，并合并重叠或首尾相接（间隔不超过 step 秒）的区间。
func MergeTimeRanges(ranges []TimeRange, step int64) []TimeRange {
	if len(ranges) == 0 {
		return nil
	}
	sorted := append([]TimeRange(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	merged := []TimeRange{sorted[0]}
	for _, next := range sorted[1:] {
		last := &merged[len(merged)-1]
		if next.Start <= last.End+step {
			last.End = max(last.End, next.End)
			continue
		}
		merged = append(merged, next)
	}
	return merged
}
