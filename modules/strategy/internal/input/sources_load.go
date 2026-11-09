package input

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// referenceLoad 是一期里一个参考市场的年龄判定输入：参考 View（带覆盖统计）与参考数据集的活跃标的。
type referenceLoad struct {
	market string
	view   ViewInfo
	active map[string]Subject
}

// loadReferences 读取参考市场的 View 与活跃标的：参考市场只做年龄探针（修订号不固定），不读行、不进帧。
func (l Loader) loadReferences(ctx context.Context, spaceID string, resolved Resolved) ([]*referenceLoad, error) {
	if len(resolved.References) == 0 {
		return nil, nil
	}
	loads := make([]*referenceLoad, 0, len(resolved.References))
	for _, market := range sortedKeys(resolved.References) {
		task := resolved.References[market]
		view, err := l.Client.GetView(ctx, spaceID, task.ViewID)
		if err != nil {
			return nil, err
		}
		if view.Status != "" && view.Status != "active" {
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("参考市场的 View %s 的状态是 %s，不再提供数据，请重新绑定实例", task.ViewID, view.Status)}
		}
		if view.ActiveIndexID == "" {
			return nil, fmt.Errorf("参考市场的 View %s 没有活动索引", task.ViewID)
		}
		if view.DatasetID != task.DatasetID || !strings.EqualFold(view.Frequency, resolved.Bar) {
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("参考市场的 View %s 的数据集或周期已变化，请重新启用实例", task.ViewID)}
		}
		if view, err = WithCoverage(ctx, l.Client, spaceID, view, false); err != nil {
			return nil, err
		}
		subjects, err := l.Client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
		if err != nil {
			return nil, err
		}
		active := make(map[string]Subject, len(subjects))
		for _, subject := range subjects {
			if subject.Active {
				active[subject.SubjectID] = subject
			}
		}
		loads = append(loads, &referenceLoad{market: market, view: view, active: active})
	}
	return loads, nil
}

// mergeSources 按 as-of 读附加来源，把来源列以 来源名.列名 并进帧里各市场的行（见 mergeSourceRows）。每个来源在每个主帧任务键上
// 读一次：只读应读的那一根（ClosedPeriod(来源日历, 来源周期, 主帧 BarEnd)），修订号不固定、读回服务端当前的修订号。
// down 里的来源本期不读（汇合 bar 上来源的因子被整体跳过），它的列按缺数处理。返回各来源 View 读到的修订号（View → 修订号）。
func (l Loader) mergeSources(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, barEnd time.Time, down map[string]string, frame *engine.Frame, unavailable map[string]string) (map[string]uint64, error) {
	if !resolved.HasSources() {
		return nil, nil
	}
	revisions := map[string]uint64{}
	for _, name := range resolved.SourceNames() {
		columns := program.SourceColumns[name]
		if len(columns) == 0 || down[name] != "" {
			continue
		}
		source := resolved.Sources[name]
		period, _, err := source.Period(barEnd)
		if err != nil {
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("来源 %s 的周期无法换算：%v", name, err)}
		}
		for _, key := range resolved.TaskKeys() {
			markets := make([]string, 0, 2)
			subjects := map[string]struct{}{}
			for _, market := range resolved.Markets() {
				if resolved.TaskKey(market) != key || unavailable[market] != "" {
					continue
				}
				markets = append(markets, market)
				for id := range frame.Markets[market].Rows {
					subjects[id] = struct{}{}
				}
			}
			if len(subjects) == 0 {
				continue
			}
			task := source.Tasks[key]
			view, err := l.Client.GetView(ctx, spaceID, task.ViewID)
			if err != nil {
				return nil, err
			}
			if view.Status != "" && view.Status != "active" {
				return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("来源 %s 的 View %s 的状态是 %s，不再提供数据，请重新绑定实例", name, task.ViewID, view.Status)}
			}
			if view.ActiveIndexID == "" {
				return nil, fmt.Errorf("来源 %s 的 View %s 没有活动索引", name, task.ViewID)
			}
			if view.DatasetID != task.DatasetID || !strings.EqualFold(view.Frequency, source.Bar) {
				return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("来源 %s 的 View %s 的数据集或周期已变化，请重新启用实例", name, task.ViewID)}
			}
			if missing := missingViewColumns(view, columns); len(missing) > 0 {
				return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("列 %s 已不存在于来源 %s 的 View %s，请重新启用实例", strings.Join(missing, "、"), name, task.ViewID)}
			}
			selected := make([]Subject, 0, len(subjects))
			for _, id := range sortedKeys(subjects) {
				selected = append(selected, Subject{SubjectID: id})
			}
			query := Query{ViewID: view.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected, Start: period.StorageStart, End: period.StorageStart.Add(time.Nanosecond), Columns: columns, ExpectedIndexID: view.ActiveIndexID}
			rows, revision, err := l.Client.QueryRows(ctx, spaceID, query)
			if err != nil {
				return nil, err
			}
			revisions[view.ViewID] = revision
			for _, market := range markets {
				byMarket, err := sourceRowsOf(rows, resolved.Unified(), market, period.StorageStart)
				if err != nil {
					return nil, err
				}
				mergeSourceRows(frame.Markets[market].Rows, name, byMarket)
			}
		}
	}
	return revisions, nil
}

// sourceRowsOf 从来源读回的行里取一个市场应读那一根（data_time 等于 start）的行，按标的归并；统一数据集按 series_tag 拆市场。
// 读到的不是应读的那一根（比应有的最新一根旧，即来源缺了一期）的行不取，这些标的的来源列按缺数处理。
func sourceRowsOf(rows []Row, unified bool, market string, start time.Time) (map[string]Row, error) {
	picked := make([]Row, 0, len(rows))
	for _, row := range rows {
		if !row.DataTime.Equal(start) {
			continue
		}
		if unified && seriesset.SeriesTagMarket(row.SeriesTag) != market {
			continue
		}
		picked = append(picked, row)
	}
	return groupRows(picked, unified, market)
}

// mergeSourceRows 把一个来源的行并进帧里一个市场的行：列名加上 来源名. 前缀；行的列表复制一份再改，不改动读回的原始行。
func mergeSourceRows(rows map[string]engine.Row, name string, source map[string]Row) {
	for id, row := range rows {
		got, ok := source[id]
		if !ok || len(got.Values) == 0 {
			continue
		}
		values := maps.Clone(row.Values)
		if values == nil {
			values = make(map[string]float64, len(got.Values))
		}
		for column, value := range got.Values {
			values[dsl.SourceColumn(name, column)] = value
		}
		row.Values = values
		rows[id] = row
	}
}

// missingViewColumns 返回 columns 里 View 已不提供的列。
func missingViewColumns(view ViewInfo, columns []string) []string {
	available := make(map[string]struct{}, len(view.Columns))
	for _, column := range view.Columns {
		available[column.Name] = struct{}{}
	}
	var missing []string
	for _, column := range columns {
		if _, ok := available[column]; !ok {
			missing = append(missing, column)
		}
	}
	return missing
}
