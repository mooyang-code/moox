package input

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/packages/factorname"
)

// 本文件是附加列来源（S7）与参考市场（S3）的绑定：
//   - 附加来源：实例为每个来源另外绑定一组因子任务，任务键与主帧相同（市场，或统一数据集的 multi），登记为 来源名:市场。
//     每根主帧按 as-of 读来源里收盘时刻不晚于这根主帧收盘时刻的最新一根，列以 来源名.列名 并进主帧的行。
//   - 参考市场：单市场策略额外绑定另一个市场的任务，只用于 age_markets: all 的年龄判定，不读行、不参与选择与下单。

// SourceBinding 是一个附加来源的绑定解析。
type SourceBinding struct {
	// Bar 与 Calendar 是来源 View 的周期与日历（日历与主帧相同）。
	Bar      string `json:"bar"`
	Calendar string `json:"calendar"`
	// Tasks 是来源在各主帧任务键上的任务（键与 Resolved.Tasks 相同）。
	Tasks map[string]TaskBinding `json:"tasks"`
	// Columns 是被引用的来源列（不带来源前缀）及其来源。
	Columns map[string]ColumnBinding `json:"columns"`
	// Factors 是来源列所属的因子成员及其契约：只在汇合 bar（主帧收盘时刻等于来源收盘时刻）上按来源的就绪事件核对。
	Factors map[string]string `json:"factors,omitempty"`
	// ViewColumns 是来源各 View 共有的列（排序，不带来源前缀）。
	ViewColumns []string `json:"view_columns"`
}

// ReadinessBinding 返回来源就绪判定用的绑定：来源不支持 bars[-1]，没有上一根的版本证据。
func (s SourceBinding) ReadinessBinding() readiness.Binding {
	factors := make(map[string]string, len(s.Factors))
	for member, contract := range s.Factors {
		factors[member] = contract
	}
	return readiness.Binding{Factors: factors}
}

// ColumnsOfFactor 返回由来源的某个因子成员产出的被引用列（不带来源前缀，排序）。
func (s SourceBinding) ColumnsOfFactor(member string) []string {
	columns := make([]string, 0)
	for name, binding := range s.Columns {
		if binding.Source == SourceFactor && binding.Member == member {
			columns = append(columns, name)
		}
	}
	sortStrings(columns)
	return columns
}

// Period 返回主帧收盘于 barEnd 的这一根应读的来源 bar（收盘时刻不晚于 barEnd 的最新一根），以及两者收盘时刻是否相同（汇合 bar）。
func (s SourceBinding) Period(barEnd time.Time) (PeriodBoundaries, bool, error) {
	period, err := ClosedPeriod(s.Calendar, s.Bar, barEnd)
	if err != nil {
		return PeriodBoundaries{}, false, err
	}
	return period, period.BarEnd.Equal(barEnd), nil
}

// HasSources 报告实例绑定了附加来源。
func (r Resolved) HasSources() bool { return len(r.Sources) > 0 }

// SourceNames 返回绑定的附加来源名（排序）。
func (r Resolved) SourceNames() []string { return sortedKeys(r.Sources) }

// Gathers 报告实例要先记下到达的事件、到齐再求值：绑定了多个主帧任务，或绑定了附加来源。只绑一个主帧任务、没有来源的实例
// 到达即求值，不写到达记录。
func (r Resolved) Gathers() bool { return len(r.Tasks) > 1 || r.HasSources() }

// ViewRole 说明一个 View 在绑定里的角色：主帧（source 为空、reference 为假）、附加来源（source 是来源名）或参考市场（reference 为真）。
// key 是主帧与来源的任务键（市场或 multi），参考市场是市场。
func (r Resolved) ViewRole(viewID string) (key, source string, reference, ok bool) {
	if key, ok := r.TaskKeyOfView(viewID); ok {
		return key, "", false, true
	}
	for _, name := range r.SourceNames() {
		for key, task := range r.Sources[name].Tasks {
			if task.ViewID == viewID {
				return key, name, false, true
			}
		}
	}
	for market, task := range r.References {
		if task.ViewID == viewID {
			return market, "", true, true
		}
	}
	return "", "", false, false
}

// ExpectedViews 返回主帧收盘于 barEnd 的这一根要等到齐的 View：主帧的全部 View，加上收盘时刻正好也是 barEnd 的来源的 View。
func (r Resolved) ExpectedViews(barEnd time.Time) ([]string, error) {
	views := r.ViewIDs()
	for _, name := range r.SourceNames() {
		source := r.Sources[name]
		_, aligned, err := source.Period(barEnd)
		if err != nil {
			return nil, err
		}
		if !aligned {
			continue
		}
		for _, key := range r.TaskKeys() {
			views = append(views, source.Tasks[key].ViewID)
		}
	}
	return views, nil
}

// AllViewIDs 返回全部绑定的 View：主帧、附加来源与参考市场（就绪记录的到达查询用）。
func (r Resolved) AllViewIDs() []string {
	views := r.ViewIDs()
	for _, name := range r.SourceNames() {
		for _, key := range r.TaskKeys() {
			views = append(views, r.Sources[name].Tasks[key].ViewID)
		}
	}
	for _, market := range sortedKeys(r.References) {
		views = append(views, r.References[market].ViewID)
	}
	return views
}

// AllTaskBindings 返回全部绑定（任务键 → 绑定）：主帧与参考市场的键是市场，附加来源的键是 来源名:市场。与实例登记的键一致。
func (r Resolved) AllTaskBindings() map[string]TaskBinding {
	tasks := make(map[string]TaskBinding, len(r.Tasks)+len(r.References))
	for key, task := range r.Tasks {
		tasks[key] = task
	}
	for market, task := range r.References {
		tasks[market] = task
	}
	for name, source := range r.Sources {
		for key, task := range source.Tasks {
			tasks[dsl.TaskKey(name, key)] = task
		}
	}
	return tasks
}

// AllTasks 返回全部绑定的因子任务（任务键 → 任务 ID，见 AllTaskBindings）。
func (r Resolved) AllTasks() map[string]string {
	bindings := r.AllTaskBindings()
	tasks := make(map[string]string, len(bindings))
	for key, task := range bindings {
		tasks[key] = task.FactorTaskID
	}
	return tasks
}

// CompileColumns 返回编译 DSL 用的全部列：主帧各 View 共有的列，加上写作 来源名.列名 的来源列。
func (r Resolved) CompileColumns() []string {
	if !r.HasSources() {
		return r.ViewColumns
	}
	columns := append([]string(nil), r.ViewColumns...)
	for _, name := range r.SourceNames() {
		for _, column := range r.Sources[name].ViewColumns {
			columns = append(columns, dsl.SourceColumn(name, column))
		}
	}
	return columns
}

// boundTasks 是按任务键拆开的实例登记：主帧（市场 → 任务）、参考市场（市场 → 任务）与附加来源（来源名 → 市场 → 任务）。
type boundTasks struct {
	main       map[string]string
	references map[string]string
	sources    map[string]map[string]string
}

// splitTasks 按 DSL 拆开实例登记的任务：来源名:市场 是附加来源，DSL 的 market.reference 写出的市场是参考市场，其余是主帧。
// 登记了 DSL 没声明的来源、或 DSL 声明的来源与参考市场没有登记任务都报错。
func splitTasks(tasks map[string]string, strategy dsl.Strategy) (boundTasks, error) {
	out := boundTasks{main: map[string]string{}, references: map[string]string{}, sources: map[string]map[string]string{}}
	declared := make(map[string]struct{}, len(strategy.Sources))
	for _, name := range strategy.SourceNames() {
		declared[name] = struct{}{}
	}
	for key, taskID := range tasks {
		source, market := dsl.SplitTaskKey(key)
		switch {
		case source != "":
			if _, ok := declared[source]; !ok {
				return boundTasks{}, fmt.Errorf("factor_tasks 登记了来源 %s 的任务（%s），但 DSL 的 sources 没有声明 %s", source, key, source)
			}
			if out.sources[source] == nil {
				out.sources[source] = map[string]string{}
			}
			out.sources[source][market] = taskID
		case slicesContains(strategy.ReferenceMarkets, market):
			out.references[market] = taskID
		default:
			out.main[market] = taskID
		}
	}
	for _, name := range strategy.SourceNames() {
		if len(out.sources[name]) == 0 {
			return boundTasks{}, fmt.Errorf("DSL 声明了来源 %s，需要为它绑定因子任务（factor_tasks 写 %s:市场=任务）", name, name)
		}
	}
	for _, market := range strategy.ReferenceMarkets {
		if _, ok := out.references[market]; !ok {
			return boundTasks{}, fmt.Errorf("DSL 写了参考市场 %s，需要绑定这个市场的因子任务（factor_tasks 写 %s=任务）", market, market)
		}
	}
	if len(out.main) == 0 {
		return boundTasks{}, errors.New("factor_tasks 没有主帧的因子任务")
	}
	return out, nil
}

func slicesContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// ResolveTasks 按实例登记的任务（任务键 → 任务）解析绑定并编译策略：主帧按 Resolve 的规则，另外解析附加来源与参考市场。
func ResolveTasks(ctx context.Context, client Client, spaceID string, tasks map[string]string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if client == nil {
		return Resolved{}, nil, errors.New("Storage 客户端未配置")
	}
	if strategy.IsPool() {
		return Resolved{}, nil, errors.New("策略池需要成员策略的定义，不能按普通策略解析")
	}
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	bound, err := splitTasks(tasks, strategy)
	if err != nil {
		return Resolved{}, nil, err
	}
	mainIDs := TaskIDs(bound.main)
	if len(mainIDs) > 2 {
		return Resolved{}, nil, fmt.Errorf("需要绑定一个或两个（现货、永续各一个）主帧因子任务，实际 %d 个", len(mainIDs))
	}
	views, err := resolveViews(ctx, client, spaceID, mainIDs, strategy.Bar)
	if err != nil {
		return Resolved{}, nil, err
	}
	return bindViewsWith(ctx, client, spaceID, views, strategy, bound)
}

// bindReferences 解析参考市场：每个参考任务的市场必须就是登记的市场，周期、日历与主帧相同。统一数据集已同时覆盖现货与永续，不能再写参考市场。
func bindReferences(ctx context.Context, client Client, spaceID string, resolved *Resolved, references map[string]string) ([]resolvedView, error) {
	if len(references) == 0 {
		return nil, nil
	}
	if resolved.Unified() {
		return nil, errors.New("统一数据集上的任务已同时覆盖现货与永续，不能再写 market.reference")
	}
	views := make([]resolvedView, 0, len(references))
	resolved.References = make(map[string]TaskBinding, len(references))
	for _, market := range sortedKeys(references) {
		item, err := resolveView(ctx, client, spaceID, strings.TrimSpace(references[market]), resolved.Bar)
		if err != nil {
			return nil, err
		}
		if item.market != market {
			return nil, fmt.Errorf("参考市场 %s 的因子任务 %s 的源数据集是 %s 市场", market, item.taskID, item.market)
		}
		if calendarOf(item) != resolved.Calendar {
			return nil, fmt.Errorf("参考市场的 View %s 的日历是 %s，与主帧的 %s 不一致", item.view.ViewID, calendarOf(item), resolved.Calendar)
		}
		resolved.References[market] = TaskBinding{ViewID: item.view.ViewID, FactorTaskID: item.taskID, DatasetID: item.view.DatasetID, SourceDatasetID: sourceDatasetID(item)}
		views = append(views, item)
	}
	return views, nil
}

// checkSourceBar 校验来源周期与主帧的关系：日历相同；加密（定长周期）要求来源周期是主帧的整数倍且更长，收盘时刻才能对齐；
// A 股的来源只能是日线，且主帧必须是日内周期。
func checkSourceBar(name, calendar, mainBar, sourceBar string) error {
	if err := CheckCalendar(calendar, sourceBar); err != nil {
		return fmt.Errorf("来源 %s 的周期 %s 在日历 %s 上不受支持：%w", name, sourceBar, calendar, err)
	}
	if normalizeCalendar(calendar) == "cn_stock" {
		if sourceBar != "1d" || mainBar == "1d" {
			return fmt.Errorf("A 股的附加来源只能是 1d，且主帧必须是日内周期：来源 %s 是 %s，主帧是 %s", name, sourceBar, mainBar)
		}
		return nil
	}
	main, err := parseBarDuration(mainBar)
	if err != nil {
		return err
	}
	source, err := parseBarDuration(sourceBar)
	if err != nil {
		return err
	}
	if source <= main || source%main != 0 {
		return fmt.Errorf("来源 %s 的周期 %s 必须是主帧周期 %s 的整数倍（且更长），收盘时刻才能对齐", name, sourceBar, mainBar)
	}
	return nil
}

// bindSources 解析附加来源：每个来源在主帧的每个任务键上各绑定一个任务（市场一致），周期与日历符合 checkSourceBar，
// DSL 引用的来源列都存在、来源 View 没有可变成员（factor_params 只作用于主帧的任务），各市场里同名列的来源一致。
func bindSources(ctx context.Context, client Client, spaceID string, resolved *Resolved, strategy dsl.Strategy, sources map[string]map[string]string, referenced []string) error {
	if len(strategy.Sources) == 0 {
		return nil
	}
	wanted := dsl.ColumnsBySource(referenced)
	keys := resolved.TaskKeys()
	resolved.Sources = make(map[string]SourceBinding, len(strategy.Sources))
	for _, declared := range strategy.Sources {
		name := declared.Name
		tasks := sources[name]
		if got := sortedKeys(tasks); strings.Join(got, ",") != strings.Join(sortedStrings(keys), ",") {
			return fmt.Errorf("来源 %s 要按主帧的任务键（%s）各绑定一个任务，实际绑定了 %s", name, strings.Join(keys, "、"), strings.Join(got, "、"))
		}
		binding := SourceBinding{Calendar: resolved.Calendar, Tasks: make(map[string]TaskBinding, len(keys)), Columns: map[string]ColumnBinding{}, Factors: map[string]string{}}
		views := make([]resolvedView, 0, len(keys))
		for _, key := range keys {
			item, err := resolveView(ctx, client, spaceID, strings.TrimSpace(tasks[key]), declared.Bar)
			if err != nil {
				return fmt.Errorf("来源 %s：%w", name, err)
			}
			if item.market != key {
				return fmt.Errorf("来源 %s 的 %s 任务 %s 的源数据集是 %s 市场", name, key, item.taskID, item.market)
			}
			if calendarOf(item) != resolved.Calendar {
				return fmt.Errorf("来源 %s 的 View %s 的日历是 %s，与主帧的 %s 不一致", name, item.view.ViewID, calendarOf(item), resolved.Calendar)
			}
			for _, column := range item.view.Columns {
				if identity, isFactor := factorColumnIdentity(column.Attributes); isFactor && identity.Kind == factorname.KindVar {
					return fmt.Errorf("来源 %s 的因子任务 %s 有可变成员 %s：factor_params 只作用于主帧的任务，来源任务不能有可变成员", name, item.taskID, identity.Member)
				}
			}
			binding.Tasks[key] = TaskBinding{ViewID: item.view.ViewID, FactorTaskID: item.taskID, DatasetID: item.view.DatasetID, SourceDatasetID: sourceDatasetID(item)}
			views = append(views, item)
		}
		binding.Bar = strings.ToLower(views[0].view.Frequency)
		if err := checkSourceBar(name, resolved.Calendar, resolved.Bar, binding.Bar); err != nil {
			return err
		}
		binding.ViewColumns = commonColumns(views)
		contracts := map[string]string{}
		for i, item := range views {
			var missing []string
			for _, column := range wanted[name] {
				if _, ok := item.columns[column]; !ok {
					missing = append(missing, column)
				}
			}
			if len(missing) > 0 {
				all := viewColumnNames(item)
				return fmt.Errorf("列 %s 不存在于来源 %s 的 View %s（因子任务 %s）；可用列（该任务的全部列）：%s", strings.Join(missing, "、"), name, item.view.ViewID, item.taskID, strings.Join(all, "、"))
			}
			for _, column := range wanted[name] {
				columnBinding := ColumnBinding{Source: SourceDataset}
				if identity, isFactor := factorColumnIdentity(item.columns[column].Attributes); isFactor {
					hash, ok := contracts[identity.FactorID]
					if !ok {
						factor, err := client.GetFactor(ctx, identity.FactorID)
						if err != nil {
							return err
						}
						if factor.DefinitionHash == "" {
							return fmt.Errorf("因子 %s 没有 definition_hash，请升级 Factor 管理端", identity.FactorID)
						}
						hash = factor.DefinitionHash
						contracts[identity.FactorID] = hash
					}
					contract := readiness.Contract(hash, identity.Kind)
					if existing, ok := binding.Factors[identity.Member]; ok && existing != contract {
						return fmt.Errorf("来源 %s 的成员 %s 在两个因子任务里的因子定义不一致，请让两个任务使用同一份成员", name, identity.Member)
					}
					binding.Factors[identity.Member] = contract
					columnBinding = ColumnBinding{Source: SourceFactor, Member: identity.Member, FactorID: identity.FactorID, OutputName: identity.Output, MemberKind: identity.Kind, DefinitionHash: hash}
				}
				if existing, ok := binding.Columns[column]; ok && i > 0 && existing != columnBinding {
					return fmt.Errorf("来源 %s 的列 %s 在两个 View 里的来源不一致，请让两个任务使用同一份成员", name, column)
				}
				binding.Columns[column] = columnBinding
			}
		}
		if len(binding.Factors) == 0 {
			binding.Factors = nil
		}
		resolved.Sources[name] = binding
	}
	return nil
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
