package input

import (
	"context"
	"errors"
	"fmt"
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

// ErrCoverageUnknown 表示 View 的覆盖统计未知：索引还没有任何行，或统计尚未就绪。
var ErrCoverageUnknown = errors.New("覆盖范围未知")

// Bounds 是按日历规整后的覆盖范围。
type Bounds struct {
	// From 是最早一根：不早于索引最早时间的周期。
	From time.Time
	// To 是最晚一根：不晚于索引最晚时间的周期（A 股夹到内嵌日历的最后一个交易日之内）。
	To time.Time
	// ToClosed 表示 To 之后还有行（A 股索引里有超出内嵌日历的数据，To 被夹到了日历的最后一个交易日）：To 这一根已经
	// 写完，可以当作完整的 bar 回放。
	ToClosed bool
}

// CoverageBounds 返回按日历规整后的覆盖范围：最早一根取不早于它的周期、最晚一根取不晚于它的周期（A 股日线按交易日，
// 并夹到内嵌日历之内——索引里可能有误写在节假日或日历之外的行）。覆盖未知返回 ErrCoverageUnknown；行全在非交易日上
// 或整体超出内嵌日历时返回说明原因的错误。
func CoverageBounds(view ViewInfo, resolved Resolved) (Bounds, error) {
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return Bounds{}, ErrCoverageUnknown
	}
	from, err := CeilStorageStart(resolved.Calendar, resolved.Bar, view.IndexedFrom)
	if err != nil {
		return Bounds{}, err
	}
	to, err := FloorStorageStart(resolved.Calendar, resolved.Bar, view.IndexedTo)
	if err != nil {
		return Bounds{}, err
	}
	if to.Before(from) {
		return Bounds{}, fmt.Errorf("索引里的行（%s 至 %s）都不在 A 股交易日上", view.IndexedFrom.UTC().Format(time.RFC3339), view.IndexedTo.UTC().Format(time.RFC3339))
	}
	return Bounds{From: from, To: to, ToClosed: beyondStockCalendar(resolved, view.IndexedTo)}, nil
}

// beyondStockCalendar 报告 A 股日线的一个行键是否晚于内嵌日历的最后一天。
func beyondStockCalendar(resolved Resolved, at time.Time) bool {
	if !stockDaily(resolved.Calendar, resolved.Bar) {
		return false
	}
	calendar, location, err := stockCalendar()
	if err != nil {
		return false
	}
	day, err := localDay(at, location)
	return err == nil && day.After(calendar.LastDate())
}

// coverageUnusable 把覆盖范围无法使用的原因写成中文说明，when 是后半句（例如“暂不能回放”）。
func coverageUnusable(viewID string, err error, when string) error {
	if errors.Is(err, ErrCoverageUnknown) {
		return fmt.Errorf("View %s 还没有数据（覆盖范围未知），%s", viewID, when)
	}
	return fmt.Errorf("View %s 的覆盖范围无法换算为周期（%w），%s", viewID, err, when)
}

// CoverageStart 返回活跃序列可回溯的最早 bar_start（以索引的最新一根为基准）；覆盖范围未知或无法换算时返回零值。
func CoverageStart(view ViewInfo, resolved Resolved) time.Time {
	return CoverageStartAt(view, resolved, time.Time{})
}

// CoverageStartAt 以 asOf 与索引最新一根中较早者为基准，返回活跃序列可回溯的最早 bar_start；覆盖范围未知或无法换算
// 时返回零值。View 每个序列至少保留最近 SeriesBars 根，而索引统计的最早时间包含已停更序列的旧行、只会提前不会推后，
// 因此取它与“基准往前 SeriesBars−1 根”中较晚者。处理第 T 根时以 T 为基准：索引已经写入更新的 bar 不影响
// 对第 T 根能否追溯的判断。局限：处理滞后跨过一次索引重建时（新一代在 T 之后构建），真实的保留起点晚于这个估计，
// 年龄探针会落到“整个数据集没有数据”的兜底判断。
func CoverageStartAt(view ViewInfo, resolved Resolved, asOf time.Time) time.Time {
	bounds, err := CoverageBounds(view, resolved)
	if err != nil {
		return time.Time{}
	}
	return coverageStart(bounds, view, resolved, asOf)
}

func coverageStart(bounds Bounds, view ViewInfo, resolved Resolved, asOf time.Time) time.Time {
	start := bounds.From
	if view.SeriesBars > 0 {
		anchor := bounds.To
		if !asOf.IsZero() && asOf.Before(anchor) {
			anchor = asOf
		}
		if bound, err := HistoryStart(resolved.Calendar, resolved.Bar, anchor, view.SeriesBars); err == nil && bound.After(start) {
			start = bound
		}
	}
	return start
}
