package input

import "time"

// CoverageStart 返回活跃序列可回溯的最早 bar_start；覆盖范围未知时返回零值。
// View 每个序列只保留最近 SeriesBars 根，而索引统计的最早时间包含已停更序列的旧行、只会提前不会推后，
// 因此取它与“最新一根往前 SeriesBars−1 根”中较晚者。回退超出日历范围时以索引统计为准。
func CoverageStart(view ViewInfo, resolved Resolved) time.Time {
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return time.Time{}
	}
	start := view.IndexedFrom
	if view.SeriesBars > 0 {
		if bound, err := HistoryStart(resolved.Calendar, resolved.Bar, view.IndexedTo, view.SeriesBars); err == nil && bound.After(start) {
			start = bound
		}
	}
	return start
}
