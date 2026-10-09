package input

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

// Loader 为一个启用实例装配一期输入。
type Loader struct {
	Client Client
}

// Bar 是一期的输入参数。
type Bar struct {
	BarStart time.Time
	// EventUniverse 为 nil 表示没有事件名单（试算），基础集合取数据集全部活跃标的；非 nil（含空列表）以名单为准。
	EventUniverse []string
	Readiness     readiness.Result
}

// Loaded 是装配完成的一期输入。
type Loaded struct {
	Frame    engine.Frame
	Sets     Sets
	Boundary PeriodBoundaries
	// IndexID 与 Revision 是本期读取固定的 View 代次，记录到输入摘要。
	IndexID  string
	Revision uint64
}

// LoadBar 读取绑定 View 的一期数据：集合划分 → 当期行（以及可选的上一根）→ 引擎帧。
// 索引或修订号变化返回 ErrStale；配置性问题返回 *SkipError；其余错误视为基础设施错误。
func (l Loader) LoadBar(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, bar Bar) (Loaded, error) {
	if l.Client == nil || program == nil {
		return Loaded{}, fmt.Errorf("输入装配缺少客户端或程序")
	}
	boundary, err := FromStorageStart(resolved.Calendar, resolved.Bar, bar.BarStart)
	if err != nil {
		return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: err.Error()}
	}
	view, err := l.Client.GetView(ctx, spaceID, resolved.ViewID)
	if err != nil {
		return Loaded{}, err
	}
	if view.Status != "" && view.Status != "active" {
		return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("View %s 的状态是 %s，不再提供数据，请重新绑定实例", resolved.ViewID, view.Status)}
	}
	if view.ActiveIndexID == "" {
		return Loaded{}, fmt.Errorf("View %s 没有活动索引", resolved.ViewID)
	}
	if view.DatasetID != resolved.DatasetID || !strings.EqualFold(view.Frequency, resolved.Bar) {
		return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("View %s 的数据集或周期已变化（%s/%s → %s/%s），请重新启用实例", resolved.ViewID, resolved.DatasetID, resolved.Bar, view.DatasetID, view.Frequency)}
	}
	if missing := MissingColumns(view, program, resolved.MinAgeBars); len(missing) > 0 {
		return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("列 %s 已不存在于 View %s，请重新启用实例", strings.Join(missing, "、"), resolved.ViewID)}
	}
	// 标签只在启用时校验过：运行期被删除时成员列表为空，exclude_tags 悄悄失效、按标签选池变空，必须报出来。
	for _, tag := range referencedTags(program.Strategy) {
		if _, err := l.Client.GetTag(ctx, spaceID, tag); err != nil {
			if errors.Is(err, ErrTagNotFound) {
				return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("DSL 引用的标签 %s 已不存在，请修改策略后重新启用实例", tag)}
			}
			return Loaded{}, err
		}
	}
	if resolved.MinAgeBars > 0 {
		// 年龄探针需要覆盖统计；事件路径只读 Storage 缓存的统计，不触发全量统计，未知时由探针的确认查询兜底。
		if view, err = WithCoverage(ctx, l.Client, spaceID, view, false); err != nil {
			return Loaded{}, err
		}
	}
	subjects, err := l.Client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
	if err != nil {
		return Loaded{}, err
	}
	activeSubjects := make(map[string]Subject, len(subjects))
	for _, subject := range subjects {
		if subject.Active {
			activeSubjects[subject.SubjectID] = subject
		}
	}
	members := func(ctx context.Context, tagID string) ([]string, error) {
		return l.Client.ListTagMembers(ctx, spaceID, tagID)
	}
	sets, err := BuildSets(ctx, members, program.Strategy, subjects, bar.EventUniverse)
	if err != nil {
		return Loaded{}, err
	}
	instruments := sets.Instruments()
	selected := make([]Subject, 0, len(instruments))
	for _, id := range instruments {
		if subject, ok := sets.Subjects[id]; ok {
			selected = append(selected, subject)
		}
	}
	columns := append([]string(nil), program.Columns...)
	columns = uniqueSorted(append(columns, program.PreviousColumns...))
	query := Query{ViewID: resolved.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected, Start: boundary.StorageStart, End: boundary.StorageStart.Add(time.Nanosecond), Columns: columns, ExpectedIndexID: view.ActiveIndexID}
	currentRows, revision, err := l.Client.QueryRows(ctx, spaceID, query)
	if err != nil {
		return Loaded{}, err
	}
	current, err := groupRows(currentRows)
	if err != nil {
		return Loaded{}, err
	}
	if err := sets.ApplyAge(ctx, resolved.MinAgeBars, NewAgeProbe(l.Client, spaceID, resolved, view, activeSubjects, bar.BarStart, revision)); err != nil {
		return Loaded{}, err
	}
	previous := map[string]Row{}
	// A 股内嵌日历的第一个交易日没有上一根（PreviousStart 为零值），bars[-1] 的值都缺失。
	if program.UsesPreviousBar && !boundary.PreviousStart.IsZero() {
		query.Start = boundary.PreviousStart
		query.End = boundary.PreviousStart.Add(time.Nanosecond)
		query.ExpectedRevision = revision
		previousRows, _, err := l.Client.QueryRows(ctx, spaceID, query)
		if err != nil {
			return Loaded{}, err
		}
		if previous, err = groupRows(previousRows); err != nil {
			return Loaded{}, err
		}
	}
	frame := engine.Frame{BarEnd: boundary.BarEnd, BarIndex: boundary.BarIndex, Spot: resolved.Spot, Rows: make(map[string]engine.Row, len(current)), Universe: sets.Universe, Expected: sets.Expected, AgedOut: sets.AgedOut, FailedColumns: failedColumns(resolved, program, bar.Readiness)}
	for subjectID, row := range current {
		if _, ok := sets.Subjects[subjectID]; !ok {
			continue
		}
		engineRow := engine.Row{Values: row.Values}
		if prev, ok := previous[subjectID]; ok {
			engineRow.Previous = prev.Values
		}
		frame.Rows[subjectID] = engineRow
	}
	return Loaded{Frame: frame, Sets: sets, Boundary: boundary, IndexID: view.ActiveIndexID, Revision: revision}, nil
}

// MissingColumns 返回策略需要、但 View 已不提供的列：当期列、bars[-1] 列，以及 min_age_bars 探针读取的 close。
func MissingColumns(view ViewInfo, program *dsl.Program, minAgeBars int) []string {
	available := make(map[string]struct{}, len(view.Columns))
	for _, column := range view.Columns {
		available[column.Name] = struct{}{}
	}
	required := append(append([]string(nil), program.Columns...), program.PreviousColumns...)
	if minAgeBars > 0 {
		required = append(required, ageProbeColumn)
	}
	var missing []string
	for _, column := range uniqueSorted(required) {
		if _, ok := available[column]; !ok {
			missing = append(missing, column)
		}
	}
	return missing
}

// groupRows 按标的归并行；同一标的出现多个序列标签视为配置错误。
func groupRows(rows []Row) (map[string]Row, error) {
	grouped := make(map[string]Row, len(rows))
	tags := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.SubjectID == "" {
			continue
		}
		if tag, seen := tags[row.SubjectID]; seen && tag != row.SeriesTag {
			return nil, &SkipError{Reason: SkipAmbiguousSeries, Detail: fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag)}
		}
		tags[row.SubjectID] = row.SeriesTag
		grouped[row.SubjectID] = row
	}
	return grouped, nil
}

// failedColumns 把就绪判定的失败标的映射到列：因子失败 → 该因子产出的被引用列（factor_failed）；
// View 级失败（K 线 View 声明的失败标的）→ 全部引用列（source_failed）。
func failedColumns(resolved Resolved, program *dsl.Program, result readiness.Result) map[string]map[string]string {
	failed := make(map[string]map[string]string)
	add := func(column, subject, reason string) {
		if failed[column] == nil {
			failed[column] = make(map[string]string)
		}
		if _, exists := failed[column][subject]; !exists {
			failed[column][subject] = reason
		}
	}
	for factorID, subjects := range result.FailedByFactor {
		for _, column := range resolved.ColumnsOfFactor(factorID) {
			for _, subject := range subjects {
				add(column, subject, engine.FailedFactor)
			}
		}
	}
	if len(result.FailedSubjects) > 0 {
		all := uniqueSorted(append(append([]string(nil), program.Columns...), program.PreviousColumns...))
		for _, column := range all {
			for _, subject := range result.FailedSubjects {
				add(column, subject, engine.FailedSource)
			}
		}
	}
	return failed
}
