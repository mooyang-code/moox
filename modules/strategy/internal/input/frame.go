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
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/seriesset"
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
	// EventUniverse 是各市场 View 就绪事件里的标的名单（市场 → 名单）。没有某个市场的条目（含整个为 nil，如试算）表示没有事件名单，
	// 该市场的基础集合取数据集全部活跃标的；有条目（含空列表）以名单为准。
	EventUniverse map[string][]string
	// Readiness 是各市场 View 的就绪结论（市场 → 结论）。
	Readiness map[string]readiness.Result
	// Unavailable 是本期整体不可用的市场（市场 → 原因）：那个市场的因子本期被跳过，不读它的行，它的标的记缺失原因“市场不可用”，
	// 已持仓的标的冻结。
	Unavailable map[string]string
}

// Loaded 是装配完成的一期输入。
type Loaded struct {
	Frame    engine.Frame
	Sets     Sets
	Boundary PeriodBoundaries
	// Steps 是读取各步骤的耗时（按发生顺序），用来定位一期读取时间花在哪。
	Steps []Step
}

// hasRows 报告任一市场本期有行。
func (l Loaded) hasRows() bool {
	for _, market := range l.Frame.Markets {
		if len(market.Rows) > 0 {
			return true
		}
	}
	return false
}

// Step 是一个读取步骤及其耗时。
type Step struct {
	Name string
	Took time.Duration
}

// marketLoad 是 LoadBar 读取一个市场的中间结果。
type marketLoad struct {
	market string
	// key 是这个市场的行所在的任务键（方式 A 就是市场自己，方式 B 是 multi，两个市场共用统一数据集的一个 View）。
	key  string
	view ViewInfo
	// active 是该市场的活跃标的；universe 是本期该市场的基础集合（事件名单 ∩ 活跃标的）。
	active   map[string]Subject
	universe map[string]Subject
	// unavailable 非空表示该市场本期整体不可用（不读行）。
	unavailable string
	revision    uint64
	current     map[string]Row
	previous    map[string]Row
}

// LoadBar 读取绑定 View 的一期数据：集合划分 → 当期行（以及可选的上一根）→ 引擎帧。绑定了多个 View 时每个市场读一份，
// 引擎帧按市场分别带行，选币池构造与定市场在引擎里完成（见 engine.Evaluate）。统一数据集（方式 B）只有一个 View：读一次全部序列，
// 按 series_tag 对应的市场拆成各市场的行，活跃状态与标的全集按市场的标签成员判断，之后与方式 A 走同一条路径。
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
	markets := resolved.Markets()
	// 一个任务键一个结果 View：方式 A 里键就是市场，方式 B 里统一数据集的 View 服务策略涉及的全部市场。
	keys := resolved.TaskKeys()
	marketsOfKey := make(map[string][]string, len(keys))
	for _, market := range markets {
		key := resolved.TaskKey(market)
		marketsOfKey[key] = append(marketsOfKey[key], market)
	}
	// keyDown 报告一个 View 服务的市场本期都不可用（不读它）。
	keyDown := func(key string) bool {
		for _, market := range marketsOfKey[key] {
			if bar.Unavailable[market] == "" {
				return false
			}
		}
		return true
	}
	// 标的修订号与 View 彼此独立：并行读取，View 校验之后再等修订号结果。数据集取启用时固化的绑定，
	// 与 View 实际的数据集不一致时下面的校验会先报配置变化。
	revalidator, canRevalidate := l.Client.(interface {
		Revalidate(ctx context.Context, spaceID, datasetID string) error
	})
	revalidated := make(map[string]chan error, len(keys))
	if canRevalidate {
		for _, key := range keys {
			if keyDown(key) {
				continue
			}
			done := make(chan error, 1)
			revalidated[key] = done
			go func(datasetID string) { done <- revalidator.Revalidate(ctx, spaceID, datasetID) }(resolved.Tasks[key].DatasetID)
		}
	}
	views := make(map[string]ViewInfo, len(keys))
	for _, key := range keys {
		task := resolved.Tasks[key]
		if keyDown(key) {
			views[key] = ViewInfo{ViewID: task.ViewID, DatasetID: task.DatasetID, Frequency: resolved.Bar}
			continue
		}
		began := time.Now()
		view, err := l.Client.GetView(ctx, spaceID, task.ViewID)
		mark("View", began)
		if err != nil {
			return Loaded{}, err
		}
		if view.Status != "" && view.Status != "active" {
			return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("View %s 的状态是 %s，不再提供数据，请重新绑定实例", task.ViewID, view.Status)}
		}
		if view.ActiveIndexID == "" {
			return Loaded{}, fmt.Errorf("View %s 没有活动索引", task.ViewID)
		}
		if view.DatasetID != task.DatasetID || !strings.EqualFold(view.Frequency, resolved.Bar) {
			return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("View %s 的数据集或周期已变化（%s/%s → %s/%s），请重新启用实例", task.ViewID, task.DatasetID, resolved.Bar, view.DatasetID, view.Frequency)}
		}
		if missing := MissingColumns(view, program, resolved.MinAgeBars); len(missing) > 0 {
			return Loaded{}, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("列 %s 已不存在于 View %s，请重新启用实例", strings.Join(missing, "、"), task.ViewID)}
		}
		views[key] = view
	}
	loads := make([]*marketLoad, 0, len(markets))
	for _, market := range markets {
		key := resolved.TaskKey(market)
		loads = append(loads, &marketLoad{market: market, key: key, view: views[key], unavailable: bar.Unavailable[market]})
	}
	// 标的元数据缓存按 Storage 的标的修订号校验；必须先于标的绑定、标签与标签成员的读取。
	for _, key := range keys {
		if done, ok := revalidated[key]; ok {
			began := time.Now()
			err := <-done
			mark("修订号等待", began)
			if err != nil {
				return Loaded{}, err
			}
		}
	}
	began := time.Now()
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
	// 覆盖统计只在探针前用到：与标的、集合和行读取并行，探针前再取结果。一个 View 只读一次。
	covered := make(map[string]chan struct {
		view ViewInfo
		err  error
	})
	if resolved.MinAgeBars > 0 {
		for _, key := range keys {
			if keyDown(key) {
				continue
			}
			done := make(chan struct {
				view ViewInfo
				err  error
			}, 1)
			covered[key] = done
			go func(view ViewInfo) {
				withCoverage, err := WithCoverage(ctx, l.Client, spaceID, view, false)
				done <- struct {
					view ViewInfo
					err  error
				}{withCoverage, err}
			}(views[key])
		}
	}
	began = time.Now()
	var mergedActive []Subject
	var mergedUniverse []string
	hasEventUniverse := false
	unifiedSubjects := map[string]map[string][]Subject{}
	for _, load := range loads {
		var subjects []Subject
		if resolved.Unified() {
			// 统一数据集：标的与活跃状态按市场判断（标签成员加标签的市场类型），同一个 View 里每个市场各取自己的。
			byMarket, ok := unifiedSubjects[load.key]
			if !ok {
				if byMarket, err = l.unifiedMarketSubjects(ctx, spaceID, load.view.DatasetID, markets); err != nil {
					return Loaded{}, err
				}
				unifiedSubjects[load.key] = byMarket
			}
			subjects = byMarket[load.market]
		} else if subjects, err = l.Client.ListDatasetSubjects(ctx, spaceID, load.view.DatasetID); err != nil {
			return Loaded{}, err
		}
		load.active = make(map[string]Subject, len(subjects))
		for _, subject := range subjects {
			if subject.Active {
				load.active[subject.SubjectID] = subject
			}
		}
		load.universe = make(map[string]Subject, len(load.active))
		if names, ok := bar.EventUniverse[load.market]; ok && names != nil {
			hasEventUniverse = hasEventUniverse || resolved.Market.Select == dsl.MarketBoth || resolved.Market.Select == load.market
			for _, id := range names {
				if subject, ok := load.active[id]; ok {
					load.universe[id] = subject
				}
			}
		} else {
			for id, subject := range load.active {
				load.universe[id] = subject
			}
		}
		// 基础集合只由选币市场构成：select 为单个市场时，另一个市场只提供可用标记（决定下单市场），它独有的标的不进入集合。
		if resolved.Market.Select != dsl.MarketBoth && resolved.Market.Select != load.market {
			continue
		}
		mergedActive = append(mergedActive, subjects...)
		for id := range load.universe {
			mergedUniverse = append(mergedUniverse, id)
		}
	}
	mark("标的", began)
	var eventUniverse []string
	if hasEventUniverse {
		eventUniverse = uniqueSorted(mergedUniverse)
		if eventUniverse == nil {
			eventUniverse = []string{}
		}
	}
	members := func(ctx context.Context, tagID string) ([]string, error) {
		return l.Client.ListTagMembers(ctx, spaceID, tagID)
	}
	began = time.Now()
	sets, err := BuildSets(ctx, members, program.Strategy, mergedActive, eventUniverse)
	mark("集合", began)
	if err != nil {
		return Loaded{}, err
	}
	instruments := sets.Instruments()
	columns := append([]string(nil), program.Columns...)
	columns = uniqueSorted(append(columns, program.PreviousColumns...))
	if len(program.ParamRefs) > 0 {
		columns = uniqueSorted(append(columns, factorname.ParamsColumn))
	}
	// 上一根与当期合并成一次范围读取：相邻两根之间没有别的根，少一次往返，也保证两根取自同一修订号。
	start := boundary.StorageStart
	if program.UsesPreviousBar && !boundary.PreviousStart.IsZero() {
		start = boundary.PreviousStart
	}
	for _, key := range keys {
		var group []*marketLoad
		for _, load := range loads {
			if load.key == key && load.unavailable == "" {
				group = append(group, load)
			}
		}
		if len(group) == 0 {
			continue
		}
		// 每个市场要读的标的；方式 B 里一个 View 读一次全部序列（selector 不带 series_tag），再按市场拆行。
		wanted := make(map[string]map[string]struct{}, len(group))
		selectedByID := make(map[string]Subject)
		for _, load := range group {
			wanted[load.market] = make(map[string]struct{}, len(instruments))
			for _, id := range instruments {
				if subject, ok := load.universe[id]; ok {
					wanted[load.market][id] = struct{}{}
					if resolved.Unified() {
						subject.SeriesTag = ""
					}
					selectedByID[id] = subject
				}
			}
		}
		selected := make([]Subject, 0, len(selectedByID))
		for _, id := range sortedKeys(selectedByID) {
			selected = append(selected, selectedByID[id])
		}
		first := group[0]
		query := Query{ViewID: first.view.ViewID, DatasetID: first.view.DatasetID, Frequency: first.view.Frequency, Subjects: selected, Start: start, End: boundary.StorageStart.Add(time.Nanosecond), Columns: columns, ExpectedIndexID: first.view.ActiveIndexID}
		began = time.Now()
		rangeRows, revision, err := l.Client.QueryRows(ctx, spaceID, query)
		mark("当期与上一根行", began)
		if err != nil {
			return Loaded{}, err
		}
		perMarket := make(map[string][]Row, len(group))
		for _, row := range rangeRows {
			market := ""
			if resolved.Unified() {
				if row.SubjectID == "" {
					continue
				}
				market = seriesset.SeriesTagMarket(row.SeriesTag)
				if market != dsl.MarketSpot && market != dsl.MarketSwap {
					if involvedIn(wanted, row.SubjectID) {
						return Loaded{}, &SkipError{Reason: SkipAmbiguousSeries, Detail: fmt.Sprintf("标的 %s 的序列 %q 无法判断所属市场", row.SubjectID, row.SeriesTag)}
					}
					continue
				}
				if _, ok := wanted[market][row.SubjectID]; !ok {
					continue
				}
			} else {
				market = first.market
			}
			perMarket[market] = append(perMarket[market], row)
		}
		for _, load := range group {
			var currentRows, previousRows []Row
			for _, row := range perMarket[load.market] {
				switch {
				case row.DataTime.Equal(boundary.StorageStart):
					currentRows = append(currentRows, row)
				case !boundary.PreviousStart.IsZero() && row.DataTime.Equal(boundary.PreviousStart):
					previousRows = append(previousRows, row)
				}
			}
			if load.current, err = groupRows(currentRows, resolved.Unified(), load.market); err != nil {
				return Loaded{}, err
			}
			load.revision = revision
			if len(previousRows) > 0 {
				if load.previous, err = groupRows(previousRows, resolved.Unified(), load.market); err != nil {
					return Loaded{}, err
				}
			}
		}
	}
	for _, key := range keys {
		if done, ok := covered[key]; ok {
			began = time.Now()
			result := <-done
			mark("覆盖统计等待", began)
			if result.err != nil {
				return Loaded{}, result.err
			}
			for _, load := range loads {
				if load.key == key {
					load.view = result.view
				}
			}
		}
	}
	began = time.Now()
	agePassed := make(map[string]map[string]struct{}, len(loads))
	err = sets.ApplyAge(ctx, resolved.MinAgeBars, func(ctx context.Context, ids []string) (map[string]struct{}, error) {
		passed := make(map[string]struct{}, len(ids))
		for _, load := range loads {
			if load.unavailable != "" {
				continue
			}
			var inMarket []string
			for _, id := range ids {
				if _, ok := load.universe[id]; ok {
					inMarket = append(inMarket, id)
				}
			}
			probe := NewAgeProbe(l.Client, spaceID, resolved, load.view, load.market, load.active, bar.BarStart, load.revision, l.Meta)
			got, err := probe(ctx, inMarket)
			if err != nil {
				return nil, err
			}
			agePassed[load.market] = got
			for id := range got {
				passed[id] = struct{}{}
			}
		}
		return passed, nil
	})
	mark("年龄探针", began)
	if err != nil {
		return Loaded{}, err
	}
	frame := engine.Frame{BarEnd: boundary.BarEnd, BarIndex: boundary.BarIndex, Markets: make(map[string]engine.MarketInput, len(loads)), Universe: sets.Universe, Expected: sets.Expected, AgedOut: sets.AgedOut}
	for _, load := range loads {
		input := engine.MarketInput{Rows: make(map[string]engine.Row, len(load.current))}
		for subjectID, row := range load.current {
			if _, ok := sets.Subjects[subjectID]; !ok {
				continue
			}
			// 多个市场时，某个市场里年龄不够的标的不用这个市场的行（例如老币刚上线永续，不能用现货的历史直接通过永续的年龄检查）。
			if passed, probed := agePassed[load.market]; probed && len(loads) > 1 {
				if _, inUniverse := load.universe[subjectID]; inUniverse {
					if _, ok := passed[subjectID]; !ok {
						continue
					}
				}
			}
			engineRow := engine.Row{Values: row.Values, Params: row.Params}
			if prev, ok := load.previous[subjectID]; ok {
				engineRow.Previous = prev.Values
			}
			input.Rows[subjectID] = engineRow
		}
		if load.unavailable != "" {
			input.Failed = marketUnavailable(program, load.universe)
		} else {
			input.Failed = failedColumns(resolved, program, bar.Readiness[load.market])
		}
		frame.Markets[load.market] = input
	}
	return Loaded{Frame: frame, Sets: sets, Boundary: boundary, Steps: steps}, nil
}

// involvedIn 报告标的是否是某个市场本期要读的标的。
func involvedIn(wanted map[string]map[string]struct{}, subjectID string) bool {
	for _, ids := range wanted {
		if _, ok := ids[subjectID]; ok {
			return true
		}
	}
	return false
}

// marketUnavailable 构造整个市场本期不可用的失败证据：该市场基础集合里每个标的在每个引用列上都记 market_unavailable。
func marketUnavailable(program *dsl.Program, universe map[string]Subject) map[string]map[string]string {
	failed := make(map[string]map[string]string)
	for _, column := range uniqueSorted(append(append([]string(nil), program.Columns...), program.PreviousColumns...)) {
		failed[column] = make(map[string]string, len(universe))
		for id := range universe {
			failed[column][id] = engine.FailedMarket
		}
	}
	return failed
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
	if len(program.ParamRefs) > 0 {
		required = append(required, factorname.ParamsColumn)
	}
	var missing []string
	for _, column := range uniqueSorted(required) {
		if _, ok := available[column]; !ok {
			missing = append(missing, column)
		}
	}
	return missing
}

// groupRows 按标的归并行；同一标的出现多个序列标签视为配置错误。统一数据集上的行已经按市场拆开，这里只在同一个市场内判断，
// 错误说明里带上市场（现货、永续两条序列并存是正常的，同一个市场里出现两个序列才有歧义）。
func groupRows(rows []Row, unified bool, market string) (map[string]Row, error) {
	grouped := make(map[string]Row, len(rows))
	tags := make(map[string]string, len(rows))
	for _, row := range rows {
		if row.SubjectID == "" {
			continue
		}
		if tag, seen := tags[row.SubjectID]; seen && tag != row.SeriesTag {
			detail := fmt.Sprintf("标的 %s 在同一周期有多个序列（%s、%s）", row.SubjectID, tag, row.SeriesTag)
			if unified {
				detail = fmt.Sprintf("标的 %s 在同一周期的%s市场有多个序列（%s、%s）", row.SubjectID, market, tag, row.SeriesTag)
			}
			return nil, &SkipError{Reason: SkipAmbiguousSeries, Detail: detail}
		}
		tags[row.SubjectID] = row.SeriesTag
		grouped[row.SubjectID] = row
	}
	return grouped, nil
}

// failedColumns 把就绪判定的失败标的映射到列：因子失败 → 该因子产出的被引用列（factor_failed；因 K 线空洞失败的记 factor_gap）；
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
		gap := make(map[string]bool, len(result.GapByFactor[factorID]))
		for _, subject := range result.GapByFactor[factorID] {
			gap[subject] = true
		}
		for _, column := range resolved.ColumnsOfFactor(factorID) {
			for _, subject := range subjects {
				reason := engine.FailedFactor
				if gap[subject] {
					reason = engine.FailedGap
				}
				add(column, subject, reason)
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
