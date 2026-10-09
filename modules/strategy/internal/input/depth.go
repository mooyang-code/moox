package input

import (
	"context"
	"time"
)

// WithCoverage 读取并填入 View 的覆盖统计。只有需要判断可回溯深度时才调用：启用时校验 min_age_bars、
// 每期的年龄探针（只读缓存的统计，exact=false）、回放区间（exact=true）。
func WithCoverage(ctx context.Context, client Client, spaceID string, view ViewInfo, exact bool) (ViewInfo, error) {
	coverage, err := client.ViewCoverage(ctx, spaceID, view, exact)
	if err != nil {
		return ViewInfo{}, err
	}
	view.IndexedFrom, view.IndexedTo, view.SeriesBars = coverage.IndexedFrom, coverage.IndexedTo, coverage.SeriesBars
	return view, nil
}

// CoverageBounds 返回按日历规整后的覆盖范围：最早一根取不早于它的周期、最晚一根取不晚于它的周期（A 股日线按交易日，
// 并夹到内嵌日历之内——索引里可能有误写在节假日或日历之外的行）。覆盖未知或无法规整时 ok 为 false。
func CoverageBounds(view ViewInfo, resolved Resolved) (from, to time.Time, ok bool) {
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	from, err := CeilStorageStart(resolved.Calendar, resolved.Bar, view.IndexedFrom)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	to, err = FloorStorageStart(resolved.Calendar, resolved.Bar, view.IndexedTo)
	if err != nil || to.Before(from) {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// CoverageStart 返回活跃序列可回溯的最早 bar_start（以索引的最新一根为基准）；覆盖范围未知时返回零值。
func CoverageStart(view ViewInfo, resolved Resolved) time.Time {
	return CoverageStartAt(view, resolved, time.Time{})
}

// CoverageStartAt 以 asOf 与索引最新一根中较早者为基准，返回活跃序列可回溯的最早 bar_start；覆盖范围未知时返回零值。
// View 每个序列至少保留最近 SeriesBars 根，而索引统计的最早时间包含已停更序列的旧行、只会提前不会推后，
// 因此取它与“基准往前 SeriesBars−1 根”中较晚者。处理第 T 根时以 T 为基准：索引已经写入更新的 bar 不影响
// 对第 T 根能否追溯的判断。局限：处理滞后跨过一次索引重建时（新一代在 T 之后构建），真实的保留起点晚于这个估计，
// 年龄探针会落到“整个数据集没有数据”的兜底判断。
func CoverageStartAt(view ViewInfo, resolved Resolved, asOf time.Time) time.Time {
	from, to, ok := CoverageBounds(view, resolved)
	if !ok {
		return time.Time{}
	}
	start := from
	if view.SeriesBars > 0 {
		anchor := to
		if !asOf.IsZero() && asOf.Before(anchor) {
			anchor = asOf
		}
		if bound, err := HistoryStart(resolved.Calendar, resolved.Bar, anchor, view.SeriesBars); err == nil && bound.After(start) {
			start = bound
		}
	}
	return start
}
