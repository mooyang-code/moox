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
	// To 是最晚一根：不晚于索引最晚时间的周期（A 股夹到内嵌日历的最后一个交易日之内；给了当前时间时，再夹到当前
	// 已闭合的最后一根之内——索引里误写的未来行不能让回放延伸到还没发生的 bar）。
	To time.Time
	// ToClosed 表示 To 这一根已经写完、可以当作完整的 bar 回放：当前时间已过 A 股内嵌日历最后一天的收盘，索引里还有
	// 那之后的数据，To 被夹到了日历的最后一个交易日。
	ToClosed bool
	// Beyond 是当前时间已过日历末日时，索引最晚一行超出日历最后一天的自然日数（不超过今天）：这些日子里至多各有
	// 一个交易日，真正的最新一根比 To 最多晚这么多根，估算每个序列保留的起点时要扣掉。
	Beyond int
}

// CoverageBounds 返回按日历规整后的覆盖范围：最早一根取不早于它的周期、最晚一根取不晚于它的周期（A 股日线按交易日，
// 并夹到内嵌日历之内——索引里可能有误写在节假日或日历之外的行）。now 是判断“已经闭合”的当前时间：回放与启用传入，
// 最晚一根夹到 now 时已闭合的最后一根之内；零值（实时求值的年龄探针）不做按时间的判断。覆盖未知返回
// ErrCoverageUnknown；行全在非交易日上、整体超出内嵌日历或 A 股行键不是上海零点时返回说明原因的错误。
func CoverageBounds(view ViewInfo, resolved Resolved, now time.Time) (Bounds, error) {
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return Bounds{}, ErrCoverageUnknown
	}
	if err := checkStockCoverageKey(view.IndexedTo, resolved); err != nil {
		return Bounds{}, err
	}
	from, err := CeilStorageStart(resolved.Calendar, resolved.Bar, view.IndexedFrom)
	if err != nil {
		return Bounds{}, err
	}
	to, err := FloorStorageStart(resolved.Calendar, resolved.Bar, view.IndexedTo)
	if err != nil {
		return Bounds{}, err
	}
	bounds := Bounds{From: from, To: to}
	if !now.IsZero() {
		// 索引里晚于当前已闭合最后一根的行是误写的未来数据：最晚一根夹到已闭合的最后一根。now 已过内嵌日历时推不出
		// 已闭合的周期，由下面的日历末日规则处理。
		if closed, err := ClosedPeriod(resolved.Calendar, resolved.Bar, now); err == nil && bounds.To.After(closed.StorageStart) {
			bounds.To = closed.StorageStart
		}
		if beyond := daysBeyondStockCalendar(resolved, view.IndexedTo, now); beyond > 0 {
			bounds.Beyond, bounds.ToClosed = beyond, true
		}
	}
	if bounds.To.Before(bounds.From) {
		label := func(at time.Time) string { return PeriodLabel(resolved.Calendar, resolved.Bar, at) }
		if stockDaily(resolved.Calendar, resolved.Bar) {
			return Bounds{}, fmt.Errorf("索引里的行（%s 至 %s）都不在 A 股交易日上", label(view.IndexedFrom), label(view.IndexedTo))
		}
		return Bounds{}, fmt.Errorf("索引里的行（%s 至 %s）都晚于当前已闭合的最后一根（疑似误写的未来数据）", label(view.IndexedFrom), label(view.IndexedTo))
	}
	return bounds, nil
}

// checkStockCoverageKey 要求 A 股日线索引统计的最晚行键是上海时间零点：按 UTC 零点写行的数据集规整后能通过提交，
// 执行时却按上海零点取不到任何行，每根都记缺数据。只看最晚一行：最早一行可能来自早已停更的序列，一行遗留的旧数据
// 不应让整个 View 永远不能使用。
func checkStockCoverageKey(at time.Time, resolved Resolved) error {
	if !stockDaily(resolved.Calendar, resolved.Bar) {
		return nil
	}
	_, location, err := stockCalendar()
	if err != nil {
		return err
	}
	day, err := localDay(at, location)
	if err != nil {
		return err
	}
	return checkStockRowKey(at, day, location)
}

// daysBeyondStockCalendar 在 now 已过 A 股内嵌日历最后一天的收盘时，返回行键 at 晚于日历最后一天的自然日数（不超过
// now 当天）；其余情况为 0。now 还在日历之内时，日历之外的行只能是误写的未来数据，不算超出日历。
func daysBeyondStockCalendar(resolved Resolved, at, now time.Time) int {
	if !stockDaily(resolved.Calendar, resolved.Bar) {
		return 0
	}
	calendar, location, err := stockCalendar()
	if err != nil {
		return 0
	}
	last := calendar.LastDate()
	lastMidnight := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, location)
	if !now.After(lastMidnight.Add(15 * time.Hour)) {
		return 0
	}
	day, err := localDay(at, location)
	if err != nil || !day.After(last) {
		return 0
	}
	latest := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
	if today := now.In(location); latest.After(today) {
		latest = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	}
	return int(latest.Sub(lastMidnight).Hours()/24 + 0.5)
}

// coverageUnusable 把覆盖范围无法使用的原因写成中文说明，when 是后半句（例如“暂不能回放”）。
func coverageUnusable(viewID string, err error, when string) error {
	if errors.Is(err, ErrCoverageUnknown) {
		return fmt.Errorf("View %s 还没有数据（覆盖范围未知），%s", viewID, when)
	}
	return fmt.Errorf("View %s 的覆盖范围无法换算为周期（%w），%s", viewID, err, when)
}

// CoverageStartAt 以 asOf 与索引最新一根中较早者为基准，返回活跃序列可回溯的最早 bar_start；覆盖范围未知或无法换算
// 时返回零值。View 每个序列至少保留最近 SeriesBars 根，而索引统计的最早时间包含已停更序列的旧行、只会提前不会推后，
// 因此取它与“基准往前 SeriesBars−1 根”中较晚者。处理第 T 根时以 T 为基准：索引已经写入更新的 bar 不影响
// 对第 T 根能否追溯的判断。局限：处理滞后跨过一次索引重建时（新一代在 T 之后构建），真实的保留起点晚于这个估计，
// 年龄探针会落到“整个数据集没有数据”的兜底判断。
func CoverageStartAt(view ViewInfo, resolved Resolved, asOf time.Time) time.Time {
	bounds, err := CoverageBounds(view, resolved, time.Time{})
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
		// 真正的最新一根可能在日历之外更晚的位置，每个序列保留的最近根数要先扣掉日历之外至多的根数（保守估计）。
		keep := view.SeriesBars
		if anchor.Equal(bounds.To) {
			keep -= bounds.Beyond
		}
		if keep <= 0 {
			// 日历之内一根都不能确定还保留着：覆盖起点放到 To 之后，调用方据此报历史不足。
			return bounds.To.Add(time.Millisecond)
		}
		if bound, err := HistoryStart(resolved.Calendar, resolved.Bar, anchor, keep); err == nil && bound.After(start) {
			start = bound
		}
	}
	return start
}
