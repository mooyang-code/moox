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
	// Meta 是进程级标的元数据缓存；为空时每个事件使用新的缓存。
	Meta *MetaCache
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
	// Steps 是读取各步骤的耗时（按发生顺序），用来定位一期读取时间花在哪。
	Steps []Step
}

// Step 是一个读取步骤及其耗时。
type Step struct {
	Name string
	Took time.Duration
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
	var steps []Step
	mark := func(name string, began time.Time) { steps = append(steps, Step{Name: name, Took: time.Since(began)}) }
	// 标的修订号与 View 彼此独立：并行读取，View 校验之后再等修订号结果。数据集取启用时固化的绑定，
	// 与 View 实际的数据集不一致时下面的校验会先报配置变化。
	revalidator, canRevalidate := l.Client.(interface {
		Revalidate(ctx context.Context, spaceID, datasetID string) error
	})
	var revalidated chan error
	if canRevalidate {
		revalidated = make(chan error, 1)
		go func() { revalidated <- revalidator.Revalidate(ctx, spaceID, resolved.DatasetID) }()
	}
	began := time.Now()
	view, err := l.Client.GetView(ctx, spaceID, resolved.ViewID)
	mark("View", began)
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
	// 标的元数据缓存按 Storage 的标的修订号校验；必须先于标的绑定、标签与标签成员的读取。
	if canRevalidate {
		began = time.Now()
		err := <-revalidated
		mark("修订号等待", began)
		if err != nil {
			return Loaded{}, err
		}
	}
	began = time.Now()
	// 标签只在启用时校验过：运行期被删除时成员列表为空，exclude_tags 悄悄失效、按标签选池变空，必须报出来。
	for _, tag := range referencedTags(program.Strategy) {
		if _, err := l.Client.GetTag(ctx, spaceID, tag); err != nil {
			if errors.Is(err, ErrTagNotFound) {
				return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("DSL 引用的标签 %s 已不存在，请修改策略后重新启用实例", tag)}
			}
			return Loaded{}, err
		}
	}
	mark("标签", began)
	// 年龄探针需要覆盖统计；事件路径只读 Storage 缓存的统计，不触发全量统计，未知时由探针的确认查询兜底。
	// 覆盖统计只在探针前用到：与标的、集合和行读取并行，探针前再取结果。
	var covered chan struct {
		view ViewInfo
		err  error
	}
	if resolved.MinAgeBars > 0 {
		covered = make(chan struct {
			view ViewInfo
			err  error
		}, 1)
		go func(view ViewInfo) {
			withCoverage, err := WithCoverage(ctx, l.Client, spaceID, view, false)
			covered <- struct {
				view ViewInfo
				err  error
			}{withCoverage, err}
		}(view)
	}
	began = time.Now()
	subjects, err := l.Client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
	mark("标的", began)
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
	began = time.Now()
	sets, err := BuildSets(ctx, members, program.Strategy, subjects, bar.EventUniverse)
	mark("集合", began)
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
	// 上一根与当期合并成一次范围读取：相邻两根之间没有别的根，少一次往返，也保证两根取自同一修订号。
	if program.UsesPreviousBar && !boundary.PreviousStart.IsZero() {
		query.Start = boundary.PreviousStart
	}
	began = time.Now()
	rangeRows, revision, err := l.Client.QueryRows(ctx, spaceID, query)
	mark("当期与上一根行", began)
	if err != nil {
		return Loaded{}, err
	}
	var currentRows, previousRows []Row
	for _, row := range rangeRows {
		switch {
		case row.DataTime.Equal(boundary.StorageStart):
			currentRows = append(currentRows, row)
		case !boundary.PreviousStart.IsZero() && row.DataTime.Equal(boundary.PreviousStart):
			previousRows = append(previousRows, row)
		}
	}
	current, err := groupRows(currentRows)
	if err != nil {
		return Loaded{}, err
	}
	if covered != nil {
		began = time.Now()
		result := <-covered
		mark("覆盖统计等待", began)
		if result.err != nil {
			return Loaded{}, result.err
		}
		view = result.view
	}
	began = time.Now()
	err = sets.ApplyAge(ctx, resolved.MinAgeBars, NewAgeProbe(l.Client, spaceID, resolved, view, activeSubjects, bar.BarStart, revision, l.Meta))
	mark("年龄探针", began)
	if err != nil {
		return Loaded{}, err
	}
	previous := map[string]Row{}
	if len(previousRows) > 0 {
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
	return Loaded{Frame: frame, Sets: sets, Boundary: boundary, Steps: steps}, nil
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
