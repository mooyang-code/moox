package input

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// DefaultReplayMaxBars 是一次回放允许的最多 bar 数。
const DefaultReplayMaxBars = 20000

// ReplayBars 返回 bar_start 落在 [start, end) 内的全部周期，超过 limit 根返回错误。
func ReplayBars(calendar, bar string, start, end time.Time, limit int) ([]PeriodBoundaries, error) {
	if !end.After(start) {
		return nil, errors.New("回放区间必须满足 start < end")
	}
	current, err := firstBarAtOrAfter(calendar, bar, start)
	if err != nil {
		return nil, err
	}
	bars := make([]PeriodBoundaries, 0)
	for current.StorageStart.Before(end) {
		if len(bars) >= limit {
			return nil, fmt.Errorf("回放区间超过 %d 根 bar，请缩短区间", limit)
		}
		bars = append(bars, current)
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return nil, err
		}
	}
	return bars, nil
}

// firstBarAtOrAfter 返回 bar_start 不早于 at 的第一根。
func firstBarAtOrAfter(calendar, bar string, at time.Time) (PeriodBoundaries, error) {
	current, err := ClosedPeriod(calendar, bar, at)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	for current.StorageStart.Before(at) {
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return PeriodBoundaries{}, err
		}
	}
	return current, nil
}

// ReplayWindow 校验并截断回放区间 [start, end)：
// 起点不能早于 View 覆盖起点加上 min_age_bars 与 bars[-1] 所需的历史，否则报错并给出可用起点；
// 终点截到 View 最新一根（IndexedTo）之后——更晚的 bar 还没有数据，不能当作空 bar 回放。
func ReplayWindow(resolved Resolved, program *dsl.Program, view ViewInfo, start, end time.Time) (time.Time, error) {
	if !end.After(start) {
		return time.Time{}, errors.New("回放区间必须满足 start < end")
	}
	if view.IndexedFrom.IsZero() || view.IndexedTo.IsZero() {
		return time.Time{}, fmt.Errorf("View %s 的数据覆盖范围未知（索引尚未统计完成），暂不能回放", resolved.ViewID)
	}
	// 终点截到最新一根的 bar_end：它是整秒，落库按毫秒保存不会丢掉最新一根；两种日历下都满足左闭右开。
	latest, err := FromStorageStart(resolved.Calendar, resolved.Bar, view.IndexedTo)
	if err != nil {
		return time.Time{}, err
	}
	if end.After(latest.BarEnd) {
		end = latest.BarEnd
	}
	if !end.After(start) {
		return time.Time{}, fmt.Errorf("回放区间内没有数据：View %s 最新一根的 bar_start 为 %s", resolved.ViewID, view.IndexedTo.Format(time.RFC3339))
	}
	need := resolved.MinAgeBars
	if program != nil && program.UsesPreviousBar && need < 2 {
		need = 2
	}
	earliest := view.IndexedFrom
	if need > 1 {
		period, err := FromStorageStart(resolved.Calendar, resolved.Bar, view.IndexedFrom)
		if err != nil {
			return time.Time{}, err
		}
		barEnd, err := AdvanceBarEnd(resolved.Calendar, resolved.Bar, period.BarEnd, need-1)
		if err != nil {
			return time.Time{}, err
		}
		shifted, err := FromBarEnd(resolved.Calendar, resolved.Bar, barEnd)
		if err != nil {
			return time.Time{}, err
		}
		earliest = shifted.StorageStart
	}
	first, err := firstBarAtOrAfter(resolved.Calendar, resolved.Bar, start)
	if err != nil {
		return time.Time{}, err
	}
	if first.StorageStart.Before(earliest) {
		return time.Time{}, fmt.Errorf("回放起点 %s 早于 View 保留起点 %s 加上所需的 %d 根历史；可用起点为 %s", first.StorageStart.Format(time.RFC3339), view.IndexedFrom.Format(time.RFC3339), need, earliest.Format(time.RFC3339))
	}
	return end, nil
}

// RangeLoader 为回放分段读取 View 的历史行：每段固定活动索引与修订号，索引切换时刷新后整段重读。
type RangeLoader struct {
	Client   Client
	SpaceID  string
	View     ViewInfo
	Subjects []Subject
	Columns  []string
}

// RangeRows 是一段读取的结果：bar_start（Unix 秒）→ subject_id → 行；Ambiguous 记录同一标的同一周期出现多个序列的 bar，
// AmbiguousSubjects 记录这些 bar 上有歧义的标的（它们的行不能用于估值）。
type RangeRows struct {
	Bars              map[int64]map[string]Row
	Ambiguous         map[int64]string
	AmbiguousSubjects map[int64]map[string]struct{}
}

// Load 读取 [start, end) 内全部标的的行。
func (l *RangeLoader) Load(ctx context.Context, start, end time.Time) (RangeRows, error) {
	var lastErr error
	for attempt := 0; attempt < maxRangeRereads; attempt++ {
		rows, _, err := l.Client.QueryRows(ctx, l.SpaceID, Query{ViewID: l.View.ViewID, DatasetID: l.View.DatasetID, Frequency: l.View.Frequency, Subjects: l.Subjects, Start: start, End: end, Columns: l.Columns, ExpectedIndexID: l.View.ActiveIndexID})
		if err == nil {
			return groupRange(rows), nil
		}
		if !errors.Is(err, ErrStale) {
			return RangeRows{}, err
		}
		lastErr = err
		view, viewErr := l.Client.GetView(ctx, l.SpaceID, l.View.ViewID)
		if viewErr != nil {
			return RangeRows{}, viewErr
		}
		l.View.ActiveIndexID = view.ActiveIndexID
	}
	return RangeRows{}, fmt.Errorf("View %s 的索引持续变化，读取放弃：%w", l.View.ViewID, lastErr)
}

const maxRangeRereads = 3

func groupRange(rows []Row) RangeRows {
	result := RangeRows{Bars: make(map[int64]map[string]Row), Ambiguous: make(map[int64]string), AmbiguousSubjects: make(map[int64]map[string]struct{})}
	tags := make(map[int64]map[string]string)
	for _, row := range rows {
		if row.SubjectID == "" {
			continue
		}
		key := row.DataTime.Unix()
		if result.Bars[key] == nil {
			result.Bars[key] = make(map[string]Row)
			tags[key] = make(map[string]string)
		}
		if tag, seen := tags[key][row.SubjectID]; seen && tag != row.SeriesTag {
			result.Ambiguous[key] = fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag)
			if result.AmbiguousSubjects[key] == nil {
				result.AmbiguousSubjects[key] = make(map[string]struct{})
			}
			result.AmbiguousSubjects[key][row.SubjectID] = struct{}{}
			continue
		}
		tags[key][row.SubjectID] = row.SeriesTag
		result.Bars[key][row.SubjectID] = row
	}
	return result
}
