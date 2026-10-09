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

// CoverageStart 返回活跃序列可回溯的最早 bar_start（以索引的最新一根为基准）；覆盖范围未知时返回零值。
func CoverageStart(view ViewInfo, resolved Resolved) time.Time {
	return CoverageStartAt(view, resolved, view.IndexedTo)
}

// CoverageStartAt 以 asOf 与索引最新一根中较早者为基准，返回活跃序列可回溯的最早 bar_start；覆盖范围未知时返回零值。
// View 每个序列至少保留最近 SeriesBars 根，而索引统计的最早时间包含已停更序列的旧行、只会提前不会推后，
// 因此取它与“基准往前 SeriesBars−1 根”中较晚者。处理第 T 根时以 T 为基准：索引已经写入更新的 bar 不影响
// 对第 T 根能否追溯的判断。回退超出日历范围时以索引统计为准。
func CoverageStartAt(view ViewInfo, resolved Resolved, asOf time.Time) time.Time {
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return time.Time{}
	}
	start := view.IndexedFrom
	if view.SeriesBars > 0 {
		anchor := view.IndexedTo
		if !asOf.IsZero() && asOf.Before(anchor) {
			anchor = asOf
		}
		if bound, err := HistoryStart(resolved.Calendar, resolved.Bar, anchor, view.SeriesBars); err == nil && bound.After(start) {
			start = bound
		}
	}
	return start
}
