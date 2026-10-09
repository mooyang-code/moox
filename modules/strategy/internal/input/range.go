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

// ReplayBars 返回 bar_start 落在 [start, end) 内的全部周期，超过 limit 根返回错误。A 股内嵌日历的最后一个交易日之后
// 没有可知的周期，枚举到它为止。
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
		if current.NextEnd.IsZero() {
			break
		}
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
		if current.NextEnd.IsZero() {
			return PeriodBoundaries{}, fmt.Errorf("%s 之后的周期超出 A 股内嵌交易日历的范围，请更新日历数据", at.UTC().Format(time.RFC3339))
		}
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return PeriodBoundaries{}, err
		}
	}
	return current, nil
}

// ReplayWindow 校验并截断回放区间 [start, end)：
// 起点不能早于活跃序列的覆盖起点（CoverageStart）加上 min_age_bars 与 bars[-1] 所需的历史，否则报错并给出可用起点；
// 终点截到最新一根（按日历规整后的 IndexedTo）之前：更晚的 bar 还没有数据，最新一根也可能还没写完，都不能当作
// 完整的 bar 回放。
func ReplayWindow(resolved Resolved, program *dsl.Program, view ViewInfo, start, end time.Time) (time.Time, error) {
	if !end.After(start) {
		return time.Time{}, errors.New("回放区间必须满足 start < end")
	}
	if view.SeriesBars > 0 && resolved.MinAgeBars > view.SeriesBars {
		return time.Time{}, fmt.Errorf("universe.min_age_bars=%d 超过 View %s 每个序列保留的 %d 根，永远无法满足", resolved.MinAgeBars, resolved.ViewID, view.SeriesBars)
	}
	coverage := CoverageStart(view, resolved)
	_, latestStart, ok := CoverageBounds(view, resolved)
	if coverage.IsZero() || !ok {
		return time.Time{}, fmt.Errorf("View %s 还没有数据（覆盖范围未知），暂不能回放", resolved.ViewID)
	}
	// 终点截到最新一根的 bar_start（整秒，落库按毫秒保存不会多出或丢掉一根）：回放到最新一根之前为止。
	latest, err := FromStorageStart(resolved.Calendar, resolved.Bar, latestStart)
	if err != nil {
		return time.Time{}, err
	}
	if end.After(latest.StorageStart) {
		end = latest.StorageStart
	}
	if !end.After(start) {
		return time.Time{}, fmt.Errorf("回放区间内没有完整的数据：View %s 最新一根的 bar_start 为 %s，回放只到它之前", resolved.ViewID, latest.StorageStart.Format(time.RFC3339))
	}
	need := resolved.MinAgeBars
	if program != nil && program.UsesPreviousBar && need < 2 {
		need = 2
	}
	earliest := coverage
	if need > 1 {
		period, err := FromStorageStart(resolved.Calendar, resolved.Bar, coverage)
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
	if !earliest.Before(latest.StorageStart) {
		return time.Time{}, fmt.Errorf("View %s 的活跃序列当前只覆盖 %s 至 %s，不够 min_age_bars 与 bars[-1] 要求的起点之前 %d 根历史，暂不能回放", resolved.ViewID, coverage.Format(time.RFC3339), latest.StorageStart.Format(time.RFC3339), need-1)
	}
	first, err := firstBarAtOrAfter(resolved.Calendar, resolved.Bar, start)
	if err != nil {
		return time.Time{}, err
	}
	if first.StorageStart.Before(earliest) {
		return time.Time{}, fmt.Errorf("回放起点 %s 太早：View 的活跃序列从 %s 起才有数据，min_age_bars 与 bars[-1] 要求起点之前还有 %d 根历史；可用起点为 %s", first.StorageStart.Format(time.RFC3339), coverage.Format(time.RFC3339), max(need-1, 0), earliest.Format(time.RFC3339))
	}
	return end, nil
}

// IndexChangedError 表示读取期间 View 的活动索引换了一代（重建过）：新一代只保留最近的根数，调用方必须先按新索引
// 复查剩余区间，再决定是否切换过去继续读。
type IndexChangedError struct {
	View ViewInfo
}

func (e *IndexChangedError) Error() string {
	return fmt.Sprintf("View %s 的活动索引已换为 %s", e.View.ViewID, e.View.Generation)
}

// RangeLoader 为回放分段读取 View 的历史行：每段固定活动索引；活动索引换了一代时返回 *IndexChangedError，
// 不静默改读新索引。
type RangeLoader struct {
	Client   Client
	SpaceID  string
	View     ViewInfo
	Subjects []Subject
	Columns  []string
}

// RangeRows 是一段读取的结果：bar_start（Unix 秒）→ subject_id → 行；Ambiguous 记录同一标的同一周期出现多个序列的
// bar 与标的（bar_start → subject_id → 说明），这些标的在该 bar 上的行不能用于估值。
type RangeRows struct {
	Bars      map[int64]map[string]Row
	Ambiguous map[int64]map[string]string
}

// Load 读取 [start, end) 内全部标的的行。
func (l *RangeLoader) Load(ctx context.Context, start, end time.Time) (RangeRows, error) {
	var lastErr error
	transportRetries := 0
	for attempt := 0; attempt < maxRangeRereads; attempt++ {
		rows, _, err := l.Client.QueryRows(ctx, l.SpaceID, Query{ViewID: l.View.ViewID, DatasetID: l.View.DatasetID, Frequency: l.View.Frequency, Subjects: l.Subjects, Start: start, End: end, Columns: l.Columns, ExpectedIndexID: l.View.ActiveIndexID})
		if err == nil {
			return groupRange(rows), nil
		}
		// 长回放有数百次分段读取：一次传输失败不应让整个回放失败，退避后重读同一段。
		var transportErr *TransportError
		if errors.As(err, &transportErr) && transportRetries < maxRangeTransportRetries {
			transportRetries++
			attempt--
			select {
			case <-ctx.Done():
				return RangeRows{}, err
			case <-time.After(time.Duration(transportRetries) * rangeRetryBackoff):
			}
			continue
		}
		if !errors.Is(err, ErrStale) {
			return RangeRows{}, err
		}
		lastErr = err
		view, viewErr := l.Client.GetView(ctx, l.SpaceID, l.View.ViewID)
		for viewErr != nil {
			var transportErr *TransportError
			if !errors.As(viewErr, &transportErr) || transportRetries >= maxRangeTransportRetries {
				return RangeRows{}, viewErr
			}
			transportRetries++
			select {
			case <-ctx.Done():
				return RangeRows{}, viewErr
			case <-time.After(time.Duration(transportRetries) * rangeRetryBackoff):
			}
			view, viewErr = l.Client.GetView(ctx, l.SpaceID, l.View.ViewID)
		}
		if view.Generation != l.View.Generation {
			return RangeRows{}, &IndexChangedError{View: view}
		}
	}
	return RangeRows{}, fmt.Errorf("View %s 的索引持续变化，读取放弃：%w", l.View.ViewID, lastErr)
}

const (
	maxRangeRereads          = 3
	maxRangeTransportRetries = 3
)

// rangeRetryBackoff 是分段读取传输失败后的退避基数（第 n 次重试等待 n 倍）。
var rangeRetryBackoff = time.Second

func groupRange(rows []Row) RangeRows {
	result := RangeRows{Bars: make(map[int64]map[string]Row), Ambiguous: make(map[int64]map[string]string)}
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
			if result.Ambiguous[key] == nil {
				result.Ambiguous[key] = make(map[string]string)
			}
			result.Ambiguous[key][row.SubjectID] = fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag)
			continue
		}
		tags[key][row.SubjectID] = row.SeriesTag
		result.Bars[key][row.SubjectID] = row
	}
	return result
}
