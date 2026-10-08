package input

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ReplayBars 返回 bar_start 落在 [start, end) 内的全部周期，超过 limit 根返回错误。
func ReplayBars(calendar, bar string, start, end time.Time, limit int) ([]PeriodBoundaries, error) {
	if !end.After(start) {
		return nil, errors.New("回放区间必须满足 start < end")
	}
	current, err := ClosedPeriod(calendar, bar, start)
	if err != nil {
		return nil, err
	}
	for current.StorageStart.Before(start) {
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return nil, err
		}
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

// RangeLoader 为回放分段读取 View 的历史行：每段固定活动索引与修订号，索引切换时刷新后整段重读。
type RangeLoader struct {
	Client   Client
	SpaceID  string
	View     ViewInfo
	Subjects []Subject
	Columns  []string
}

// RangeRows 是一段读取的结果：bar_start（Unix 秒）→ subject_id → 行；Ambiguous 记录同一标的同一周期出现多个序列的 bar。
type RangeRows struct {
	Bars      map[int64]map[string]Row
	Ambiguous map[int64]string
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
	result := RangeRows{Bars: make(map[int64]map[string]Row), Ambiguous: make(map[int64]string)}
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
			continue
		}
		tags[key][row.SubjectID] = row.SeriesTag
		result.Bars[key][row.SubjectID] = row
	}
	return result
}
