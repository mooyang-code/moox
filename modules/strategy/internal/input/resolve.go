package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// DefaultCalendar 是没有声明日历的数据集使用的日历。
const DefaultCalendar = "crypto_24x7"

// datasetRoleFactorResult 是 Factor 写入的结果数据集角色。
const datasetRoleFactorResult = "factor_result"

// resolvedView 是解析过程中一个绑定任务的结果 View 与它的数据集。
type resolvedView struct {
	market  string
	taskID  string
	view    ViewInfo
	dataset DatasetInfo
	source  DatasetInfo
	columns map[string]ViewColumn
}

// Resolve 在启用时解析绑定：每个因子任务 → 结果 View → 数据集 → 源数据集市场类型，确定市场模式，
// 再校验两个 View 的周期、日历、列与成员契约，编译策略。taskIDs 是绑定的因子任务（一个或现货、永续各一个）。
// 策略池要先取到成员定义，走 ResolvePool。
func Resolve(ctx context.Context, client Client, spaceID string, taskIDs []string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if client == nil {
		return Resolved{}, nil, errors.New("Storage 客户端未配置")
	}
	if len(taskIDs) == 0 || len(taskIDs) > 2 {
		return Resolved{}, nil, fmt.Errorf("需要绑定一个或两个（现货、永续各一个）因子任务，实际 %d 个", len(taskIDs))
	}
	if strategy.IsPool() {
		return Resolved{}, nil, errors.New("策略池需要成员策略的定义，不能按普通策略解析")
	}
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	views, err := resolveViews(ctx, client, spaceID, taskIDs, strategy.Bar)
	if err != nil {
		return Resolved{}, nil, err
	}
	return bindViews(ctx, client, spaceID, views, strategy)
}

// resolveViews 解析每个因子任务的结果 View 与数据集，按市场先后（现货、永续）排序；两个任务的市场不能相同。bar 是 DSL 的周期断言。
func resolveViews(ctx context.Context, client Client, spaceID string, taskIDs []string, bar string) ([]resolvedView, error) {
	var views []resolvedView
	seen := map[string]string{}
	for _, taskID := range taskIDs {
		taskID = strings.TrimSpace(taskID)
		item, err := resolveView(ctx, client, spaceID, taskID, bar)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[item.market]; dup {
			return nil, fmt.Errorf("因子任务 %s 与另一个任务的市场都是 %s，两个任务必须一个现货、一个永续", taskID, item.market)
		}
		seen[item.market] = taskID
		views = append(views, item)
	}
	if _, unified := seen[MarketMulti]; unified && len(views) > 1 {
		return nil, fmt.Errorf("因子任务 %s 在统一数据集上，同时覆盖现货与永续，不能再绑定其他任务", seen[MarketMulti])
	}
	sort.Slice(views, func(i, j int) bool { return marketOrder(views[i].market) < marketOrder(views[j].market) })
	return views, nil
}

// bindViews 在已解析的 View 上绑定并编译一个策略：校验周期与日历一致、确定市场模式、校验列与成员契约。
func bindViews(ctx context.Context, client Client, spaceID string, views []resolvedView, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	resolved := Resolved{Tasks: map[string]TaskBinding{}, Columns: map[string]ColumnBinding{}, Factors: map[string]string{}, MinAgeBars: strategy.Universe.MinAgeBars, RuleAges: ruleAges(strategy)}
	for _, item := range views {
		resolved.Tasks[item.market] = TaskBinding{ViewID: item.view.ViewID, FactorTaskID: item.taskID, DatasetID: item.view.DatasetID, SourceDatasetID: sourceDatasetID(item)}
	}
	first := views[0]
	for _, item := range views[1:] {
		if !strings.EqualFold(item.view.Frequency, first.view.Frequency) {
			return Resolved{}, nil, fmt.Errorf("两个 View 的周期不一致：%s 是 %s，%s 是 %s", first.view.ViewID, first.view.Frequency, item.view.ViewID, item.view.Frequency)
		}
		if calendarOf(item) != calendarOf(first) {
			return Resolved{}, nil, fmt.Errorf("两个 View 的日历不一致：%s 是 %s，%s 是 %s", first.view.ViewID, calendarOf(first), item.view.ViewID, calendarOf(item))
		}
	}
	resolved.Bar, resolved.Calendar = strings.ToLower(first.view.Frequency), calendarOf(first)
	// 市场模式：DSL 写出的，或只绑一个任务时取任务的市场；股票不允许写 market；统一数据集同时覆盖现货与永续，没有可当默认值的市场，必须写。
	unified := first.market == MarketMulti
	switch {
	case first.market == dsl.MarketEquity && strategy.MarketDeclared:
		return Resolved{}, nil, errors.New("股票策略不允许写 market")
	case strategy.MarketDeclared:
		resolved.Market = strategy.Market
	case unified:
		return Resolved{}, nil, errors.New("因子任务在统一数据集上，同时覆盖现货与永续，DSL 必须写 market（select、prefer）")
	case len(views) == 1:
		resolved.Market = dsl.SingleMarket(first.market)
	default:
		return Resolved{}, nil, errors.New("绑定了现货、永续两个因子任务，DSL 必须写 market（select、prefer）")
	}
	if unified {
		// 参数按标的取值，现货、永续两条序列会共用同一组参数，是否合理以后再定。
		if len(strategy.FactorParams) > 0 {
			return Resolved{}, nil, errors.New("统一数据集上的任务暂不支持 factor_params：现货、永续两条序列会共用同一组参数")
		}
	} else if got := resolved.Markets(); len(got) != len(resolved.Market.Markets()) || len(got) != len(views) {
		return Resolved{}, nil, fmt.Errorf("市场模式 %s 需要绑定 %s 的因子任务，实际绑定了 %s", resolved.Market, strings.Join(resolved.Market.Markets(), "、"), strings.Join(boundMarkets(views), "、"))
	}
	strategy.Market = resolved.Market
	if err := dsl.ValidateMode(strategy, resolved.Market); err != nil {
		return Resolved{}, nil, err
	}
	if err := CheckCalendar(resolved.Calendar, resolved.Bar); err != nil {
		return Resolved{}, nil, fmt.Errorf("日历 %s 与周期 %s 的组合不受支持：%w", resolved.Calendar, resolved.Bar, err)
	}
	if err := CheckHoldingAnchors(resolved.Calendar, resolved.Bar, strategy); err != nil {
		return Resolved{}, nil, err
	}
	if err := resolved.CheckAgeMarkets(); err != nil {
		return Resolved{}, nil, err
	}
	longest := resolved.LongestAge()
	for _, item := range views {
		if bars, ok := retentionBars(item.dataset.Retention, resolved.Calendar, resolved.Bar); ok && longest > bars {
			return Resolved{}, nil, fmt.Errorf("年龄判定需要 %d 根（min_age_bars、max_age_bars 的最大值），超过 View 的保留根数 %d", longest, bars)
		}
		if longest > 0 {
			if _, ok := item.columns[ageProbeColumn]; !ok {
				return Resolved{}, nil, fmt.Errorf("min_age_bars、max_age_bars 通过 %s 列判断上市时间，但 View %s 没有该列", ageProbeColumn, item.view.ViewID)
			}
		}
	}
	viewInfos := make([]ViewInfo, 0, len(views))
	for _, item := range views {
		viewInfos = append(viewInfos, item.view)
	}
	if err := checkReferences(ctx, client, spaceID, viewInfos, strategy); err != nil {
		return Resolved{}, nil, err
	}
	referenced, err := dsl.ReferencedColumns(strategy)
	if err != nil {
		return Resolved{}, nil, err
	}
	// 先报缺列：任务刚改动成员时结果 View 的列由 Storage 异步补齐，此时可变成员也还看不全，缺列是可重试的暂态。
	resolved.ViewColumns = commonColumns(views)
	for _, item := range views {
		var missing []string
		for _, name := range referenced {
			if _, ok := item.columns[name]; !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			continue
		}
		// 列名写错是最常见的原因：先给出每个缺失列最接近的几个列名，再附上这个任务的全部列名。
		all := viewColumnNames(item)
		hints := make([]string, 0, len(missing))
		for _, name := range missing {
			if near := nearestNames(name, all, 3); len(near) > 0 {
				hints = append(hints, fmt.Sprintf("%s 可能是 %s", name, strings.Join(near, "、")))
			}
		}
		suggestion := ""
		if len(hints) > 0 {
			suggestion = "；最接近的列名：" + strings.Join(hints, "；")
		}
		return Resolved{}, nil, fmt.Errorf("列 %s 不存在于 View %s（因子任务 %s）%s；可用列（该任务的全部列）：%s", strings.Join(missing, "、"), item.view.ViewID, item.taskID, suggestion, strings.Join(all, "、"))
	}
	members := make(map[string]VarMember)
	memberViews := make([]map[string]VarMember, 0, len(views))
	for _, item := range views {
		found, err := varMembers(ctx, client, item.view)
		if err != nil {
			return Resolved{}, nil, err
		}
		memberViews = append(memberViews, found)
		for key, member := range found {
			if _, seen := members[key]; !seen {
				members[key] = member
			}
		}
	}
	refs, err := dsl.ReferencedParamRefs(strategy)
	if err != nil {
		return Resolved{}, nil, err
	}
	if err := CheckFactorParams(strategy, members, refs); err != nil {
		return Resolved{}, nil, err
	}
	for _, ref := range refs {
		for i, found := range memberViews {
			if _, ok := found[ref.Member]; !ok {
				return Resolved{}, nil, fmt.Errorf("params(%s, ...) 引用的可变成员在 View %s 里不存在", ref.Member, views[i].view.ViewID)
			}
		}
	}
	resolved.FactorParams = make(map[string][]ParamDecl, len(members))
	for key, member := range members {
		resolved.FactorParams[key] = member.Params
	}
	contracts := map[string]string{} // factor_id → definition_hash：同一定义的多个成员只读一次
	bind := func(member, factorID, kind string) (string, error) {
		hash, ok := contracts[factorID]
		if !ok {
			factor, err := client.GetFactor(ctx, factorID)
			if err != nil {
				return "", err
			}
			if factor.DefinitionHash == "" {
				return "", fmt.Errorf("因子 %s 没有 definition_hash，请升级 Factor 管理端", factorID)
			}
			hash = factor.DefinitionHash
			contracts[factorID] = hash
		}
		contract := readiness.Contract(hash, kind)
		if existing, ok := resolved.Factors[member]; ok && existing != contract {
			return "", fmt.Errorf("成员 %s 在两个因子任务里的因子定义不一致（%s 与 %s），请让两个任务使用同一份成员", member, existing, contract)
		}
		resolved.Factors[member] = contract
		return hash, nil
	}
	for i, item := range views {
		for _, name := range referenced {
			column := item.columns[name]
			binding := ColumnBinding{Source: SourceDataset}
			if identity, isFactor := factorColumnIdentity(column.Attributes); isFactor {
				hash, err := bind(identity.Member, identity.FactorID, identity.Kind)
				if err != nil {
					return Resolved{}, nil, err
				}
				binding = ColumnBinding{Source: SourceFactor, Member: identity.Member, FactorID: identity.FactorID, OutputName: identity.Output,
					MemberKind: identity.Kind, DefinitionHash: hash}
			}
			if existing, ok := resolved.Columns[name]; ok && i > 0 && existing != binding {
				return Resolved{}, nil, fmt.Errorf("列 %s 在两个 View 里的来源不一致（%s 与 %s），请让两个任务使用同一份成员", name, existing.Member, binding.Member)
			}
			resolved.Columns[name] = binding
		}
		// params() 读取的可变成员也要核对契约：成员被改动（定义变了、被换掉）后，这一期的参数不再是策略启用时看到的那个成员的。
		for _, ref := range refs {
			member := memberViews[i][ref.Member]
			if _, err := bind(ref.Member, member.FactorID, factorname.KindVar); err != nil {
				return Resolved{}, nil, err
			}
		}
	}
	program, err := dsl.Compile(strategy, resolved.ViewColumns)
	if err != nil {
		return Resolved{}, nil, err
	}
	resolved.PreviousFactors = previousFactors(resolved.Columns, program)
	return resolved, program, nil
}

// resolveView 解析一个因子任务：结果 View、数据集、源数据集与市场类型。
func resolveView(ctx context.Context, client Client, spaceID, taskID, bar string) (resolvedView, error) {
	viewID, err := ViewIDOfTask(taskID)
	if err != nil {
		return resolvedView{}, fmt.Errorf("因子任务 ID %q 不合法：%w", taskID, err)
	}
	view, err := client.GetView(ctx, spaceID, viewID)
	if err != nil {
		return resolvedView{}, err
	}
	if view.DatasetID == "" || view.Frequency == "" {
		return resolvedView{}, fmt.Errorf("View %s 没有数据集或频率", viewID)
	}
	if view.Status != "" && view.Status != "active" {
		return resolvedView{}, fmt.Errorf("View %s 的状态是 %s，只能绑定 active 的 View", viewID, view.Status)
	}
	if bar != "" && !strings.EqualFold(bar, view.Frequency) {
		return resolvedView{}, fmt.Errorf("DSL 的 bar 断言为 %s，但 View %s 的周期是 %s", bar, viewID, view.Frequency)
	}
	dataset, err := client.GetDataset(ctx, spaceID, view.DatasetID)
	if err != nil {
		return resolvedView{}, err
	}
	item := resolvedView{taskID: taskID, view: view, dataset: dataset, source: dataset, columns: make(map[string]ViewColumn, len(view.Columns))}
	if sourceID := strings.TrimSpace(dataset.Attributes["source_dataset_id"]); sourceID != "" && sourceID != dataset.DatasetID {
		if item.source, err = client.GetDataset(ctx, spaceID, sourceID); err != nil {
			return resolvedView{}, err
		}
	}
	if calendarOf(item) == "cn_stock" {
		for _, attributes := range []map[string]string{item.dataset.Attributes, item.source.Attributes} {
			if strings.EqualFold(strings.TrimSpace(attributes["alignment"]), "epoch_utc") {
				return resolvedView{}, fmt.Errorf("数据集 %s 的行按 UTC 零点对齐（alignment=epoch_utc），不能使用 A 股日历 cn_stock", view.DatasetID)
			}
		}
	}
	if item.market, err = sourceMarketType(ctx, client, spaceID, item.source); err != nil {
		return resolvedView{}, err
	}
	for _, column := range view.Columns {
		item.columns[column.Name] = column
	}
	return item, nil
}

// calendarOf 返回 View 的交易日历：数据集自己声明的；因子结果沿用源数据集的行键（同一套 bar_start），可以继承源数据集的日历，
// 重采样等派生数据集按自己的对齐方式写行，继承源日历会把行键算错；都没有时用默认日历。
func calendarOf(item resolvedView) string {
	calendar := strings.ToLower(strings.TrimSpace(item.dataset.Attributes["calendar"]))
	if calendar == "" && item.source.DatasetID != item.dataset.DatasetID && strings.TrimSpace(item.dataset.Attributes["dataset_role"]) == datasetRoleFactorResult {
		calendar = strings.ToLower(strings.TrimSpace(item.source.Attributes["calendar"]))
	}
	if calendar == "" {
		calendar = DefaultCalendar
	}
	return calendar
}

func sourceDatasetID(item resolvedView) string {
	if item.source.DatasetID != item.dataset.DatasetID {
		return item.source.DatasetID
	}
	return ""
}

func marketOrder(market string) int {
	switch market {
	case dsl.MarketSpot:
		return 0
	case dsl.MarketSwap:
		return 1
	default:
		return 2
	}
}

func boundMarkets(views []resolvedView) []string {
	markets := make([]string, 0, len(views))
	for _, item := range views {
		markets = append(markets, item.market)
	}
	return markets
}

// viewColumnNames 返回 View 全部列名（排序）。
func viewColumnNames(item resolvedView) []string {
	names := make([]string, 0, len(item.columns))
	for name := range item.columns {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// commonColumns 返回所有 View 共有的列名（排序）。
func commonColumns(views []resolvedView) []string {
	var common []string
	for name := range views[0].columns {
		shared := true
		for _, item := range views[1:] {
			if _, ok := item.columns[name]; !ok {
				shared = false
				break
			}
		}
		if shared {
			common = append(common, name)
		}
	}
	sortStrings(common)
	return common
}

// checkReferences 校验 DSL 引用的标签存在、固定写出的标的（pool 列表、universe 的 include 与 exclude）
// 在 View 的数据集中有绑定。ID 区分大小写；拼写错误会让池悄悄变空或排除失效，必须在启用时报出。
func checkReferences(ctx context.Context, client Client, spaceID string, views []ViewInfo, strategy dsl.Strategy) error {
	instruments := make([]string, 0)
	instruments = append(instruments, strategy.Universe.Include...)
	instruments = append(instruments, strategy.Universe.Exclude...)
	for _, rule := range strategy.Rules {
		instruments = append(instruments, rule.Pool.Fixed...)
		if rule.Universe != nil {
			instruments = append(instruments, rule.Universe.Include...)
			instruments = append(instruments, rule.Universe.Exclude...)
		}
	}
	for _, tag := range referencedTags(strategy) {
		if _, err := client.GetTag(ctx, spaceID, tag); err != nil {
			if errors.Is(err, ErrTagNotFound) {
				return fmt.Errorf("DSL 引用的标签 %s 不存在（ID 区分大小写）", tag)
			}
			return err
		}
	}
	if len(instruments) == 0 {
		return nil
	}
	// 固定写出的标的只要在任一绑定的 View 的数据集里就行（现货、永续的标的范围可以不同）。
	known := make(map[string]struct{})
	names := make([]string, 0, len(views))
	for _, view := range views {
		subjects, err := client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
		if err != nil {
			return err
		}
		for _, subject := range subjects {
			known[subject.SubjectID] = struct{}{}
		}
		names = append(names, view.ViewID)
	}
	var unknown []string
	for _, id := range uniqueSorted(instruments) {
		if _, ok := known[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("DSL 写出的标的 %s 不在 View %s 的数据集中（ID 区分大小写）", strings.Join(unknown, "、"), strings.Join(names, "、"))
	}
	return nil
}

// referencedTags 返回 DSL 引用的全部标签（去重、排序）。
func referencedTags(strategy dsl.Strategy) []string {
	tags := append(append([]string(nil), strategy.Universe.Tags...), strategy.Universe.ExcludeTags...)
	for _, rule := range strategy.Rules {
		tags = append(tags, rule.Pool.Tags...)
		if rule.Universe != nil {
			tags = append(tags, rule.Universe.Tags...)
			tags = append(tags, rule.Universe.ExcludeTags...)
		}
	}
	return uniqueSorted(tags)
}

// CheckCoverage 在启用实例时校验 View 的覆盖（需要读取覆盖统计，统计未缓存时现算）：A 股日线总是校验行键是交易日的
// 上海零点（按 UTC 零点写行的数据集启用后每一期都会被丢弃）；设置了 min_age_bars 时校验能追溯所需的历史。两者都不
// 涉及时不读取。
//
// 校验的是关联任务的结果 View：它的历史由因子计算与手动补算写出，所以 min_age_bars 需要的历史也要先补算出来。
func CheckCoverage(ctx context.Context, client Client, spaceID string, resolved Resolved, now time.Time) error {
	if resolved.LongestAge() <= 0 && !stockDaily(resolved.Calendar, resolved.Bar) {
		return nil
	}
	for _, key := range resolved.TaskKeys() {
		if err := checkViewCoverage(ctx, client, spaceID, resolved, resolved.Tasks[key].ViewID, now); err != nil {
			return err
		}
	}
	return nil
}

// checkViewCoverage 校验一个绑定 View 的覆盖。
func checkViewCoverage(ctx context.Context, client Client, spaceID string, resolved Resolved, viewID string, now time.Time) error {
	view, err := client.GetView(ctx, spaceID, viewID)
	if err != nil {
		return err
	}
	view.ViewID = viewID
	if view, err = WithCoverage(ctx, client, spaceID, view, true); err != nil {
		return err
	}
	if longest := resolved.LongestAge(); longest > 0 {
		if err := checkAgeCoverage(view, resolved, longest, now); err != nil {
			return err
		}
		// 统一数据集的 View 级覆盖统计取所有序列的最早一行：现货、永续分两次导入、永续历史较短时它会放行，之后永续一直被当新上市。
		// 各市场的历史分别要够：在目标根附近按市场探针，某个市场一行都没有就拒绝启用（与现货、永续各一个任务时分别校验一致）。
		if resolved.Unified() {
			return checkUnifiedAgeCoverage(ctx, client, spaceID, view, resolved, now)
		}
		return nil
	}
	// 没有 min_age_bars 时覆盖范围不影响实时求值，只校验行键；还没有数据时无从校验，允许启用。
	if view.IndexedTo.IsZero() {
		return nil
	}
	if err := checkStockCoverageKey(view.IndexedTo, resolved); err != nil {
		return coverageUnusable(viewID, err, "不能启用实例")
	}
	return nil
}

// checkAgeCoverage 校验 View 能追溯 min_age_bars：N 不超过每个序列保留的根数，且以最新一根为 T，
// T − (N−1) 根不早于活跃序列的覆盖起点。覆盖范围未知（索引还没有任何行）时无法确认，拒绝启用。
func checkAgeCoverage(view ViewInfo, resolved Resolved, minAgeBars int, now time.Time) error {
	if view.SeriesBars > 0 && minAgeBars > view.SeriesBars {
		return fmt.Errorf("universe.min_age_bars=%d 超过 View %s 每个序列保留的 %d 根，永远无法满足", minAgeBars, view.ViewID, view.SeriesBars)
	}
	bounds, err := CoverageBounds(view, resolved, now)
	if errors.Is(err, ErrCoverageUnknown) {
		return fmt.Errorf("View %s 还没有数据（覆盖范围未知），无法确认 universe.min_age_bars=%d 所需的历史，请等数据写入后再启用", view.ViewID, minAgeBars)
	}
	if err != nil {
		return coverageUnusable(view.ViewID, err, fmt.Sprintf("无法确认 universe.min_age_bars=%d 所需的历史", minAgeBars))
	}
	label := func(at time.Time) string { return PeriodLabel(resolved.Calendar, resolved.Bar, at) }
	// 启用前已确认当前时间没有过内嵌日历的可用截止日，索引最晚一行不会超出日历（Beyond 为 0）。
	start, latest := coverageStart(bounds, view, resolved, time.Time{}), bounds.To
	target, err := HistoryStart(resolved.Calendar, resolved.Bar, latest, minAgeBars)
	if errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
		return fmt.Errorf("universe.min_age_bars=%d 需要追溯到 A 股内嵌交易日历的起点之前，但 View %s 的活跃序列当前只覆盖 %s 至 %s；请减小 min_age_bars",
			minAgeBars, view.ViewID, label(start), label(latest))
	}
	if err != nil {
		return err
	}
	if target.Before(start) {
		return fmt.Errorf("universe.min_age_bars=%d 需要追溯到 %s，但 View %s 的活跃序列当前只覆盖 %s 至 %s；请减小 min_age_bars 或等待 View 积累更多历史",
			minAgeBars, label(target), view.ViewID, label(start), label(latest))
	}
	return nil
}

// sourceMarketType 判定源数据集的市场类型：优先读数据集属性 market_type，否则由其标的范围标签的 market_type 决定；
// 标签缺失、不一致或取值未知都拒绝启用，避免把合约当作现货（或相反）。
func sourceMarketType(ctx context.Context, client Client, spaceID string, source DatasetInfo) (string, error) {
	if value := strings.ToLower(strings.TrimSpace(source.Attributes["market_type"])); value != "" {
		if !knownMarketType(value) {
			return "", fmt.Errorf("源数据集 %s 的 market_type=%s 不受支持", source.DatasetID, value)
		}
		return value, nil
	}
	if len(source.SubjectTags) == 0 {
		return "", fmt.Errorf("源数据集 %s 既没有 market_type 属性也没有标的范围标签，无法确定市场类型", source.DatasetID)
	}
	marketType := ""
	for _, tagID := range source.SubjectTags {
		tag, err := client.GetTag(ctx, spaceID, tagID)
		if err != nil {
			return "", err
		}
		if !knownMarketType(tag.MarketType) {
			return "", fmt.Errorf("标签 %s 的市场类型 %q 不受支持", tagID, tag.MarketType)
		}
		if marketType != "" && marketType != tag.MarketType {
			return "", fmt.Errorf("源数据集 %s 的标签同时包含 %s 与 %s，无法确定市场类型", source.DatasetID, marketType, tag.MarketType)
		}
		marketType = tag.MarketType
	}
	return marketType, nil
}

// knownMarketType 报告策略支持的数据集市场类型：现货 spot、永续合约 swap、A 股 equity，以及同时放现货与永续的统一数据集 multi。
func knownMarketType(value string) bool {
	return value == "spot" || value == "swap" || value == "equity" || value == MarketMulti
}

// StockTradingDaysPerYear 是 A 股一年的交易日数（与回放年化口径一致），用于把自然时长折算为交易日根数。
const StockTradingDaysPerYear = 244

// retentionBars 把数据集保留期（"<n>h" 或 "forever"）换算为根数；A 股先按交易日折算，再乘每个交易日的根数（日线 1，
// 5m 为 48）。forever 或无法解析返回 false。
func retentionBars(retention, calendar, bar string) (int, bool) {
	retention = strings.TrimSpace(strings.ToLower(retention))
	if retention == "" || retention == "forever" || !strings.HasSuffix(retention, "h") {
		return 0, false
	}
	hours, err := strconv.Atoi(strings.TrimSuffix(retention, "h"))
	if err != nil || hours <= 0 {
		return 0, false
	}
	if perDay := TradingDayBars(calendar, bar); perDay > 0 {
		return hours / 24 * StockTradingDaysPerYear / 365 * perDay, true
	}
	duration, err := parseBarDuration(bar)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return int((time.Duration(hours) * time.Hour) / duration), true
}

// factorIdentity 是因子输出列的身份：成员键、引用的定义、输出名与成员类型。
type factorIdentity struct {
	Member, FactorID, Output, Kind string
}

// factorColumnIdentity 返回因子输出列（column_kind=output）的身份；__params 等不是因子输出的列返回 false。
func factorColumnIdentity(attributes map[string]string) (factorIdentity, bool) {
	if strings.TrimSpace(attributes["column_kind"]) != factorname.ColumnKindOutput {
		return factorIdentity{}, false
	}
	member := strings.TrimSpace(attributes["member_key"])
	if member == "" {
		return factorIdentity{}, false
	}
	return factorIdentity{Member: member, FactorID: strings.TrimSpace(attributes["factor_id"]),
		Output: strings.TrimSpace(attributes["output_name"]), Kind: strings.TrimSpace(attributes["member_kind"])}, true
}

// varMembers 从任务结果 View 的列属性里找出任务的可变成员，并读它们的因子定义声明的可调参数。
func varMembers(ctx context.Context, client Client, view ViewInfo) (map[string]VarMember, error) {
	members := make(map[string]VarMember)
	definitions := make(map[string]FactorInfo)
	for _, column := range view.Columns {
		identity, ok := factorColumnIdentity(column.Attributes)
		if !ok || identity.Kind != factorname.KindVar {
			continue
		}
		if _, seen := members[identity.Member]; seen {
			continue
		}
		info, ok := definitions[identity.FactorID]
		if !ok {
			var err error
			if info, err = client.GetFactor(ctx, identity.FactorID); err != nil {
				return nil, err
			}
			definitions[identity.FactorID] = info
		}
		members[identity.Member] = VarMember{Key: identity.Member, FactorID: identity.FactorID, Params: info.Params}
	}
	return members, nil
}

// previousFactors 返回经 bars[-1] 读取的因子列所属的成员（排序）；没有时返回 nil。
func previousFactors(columns map[string]ColumnBinding, program *dsl.Program) []string {
	ids := make([]string, 0)
	for _, column := range program.PreviousColumns {
		if binding := columns[column]; binding.Source == SourceFactor && binding.Member != "" {
			ids = append(ids, binding.Member)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return uniqueSorted(ids)
}

// Compile 用固化的解析结果重新编译 DSL（重启或恢复会话时使用），并按编译结果重算派生字段（经 bars[-1] 读取的因子）：
// 它们不进快照，不会因快照缺字段而静默跳过上一根的版本核对。编译失败时原样返回传入的解析结果：调用方仍要用其中的
// 日历与周期记录跳过。
func Compile(resolved Resolved, dslYaml string) (Resolved, *dsl.Program, error) {
	strategy, err := dsl.Parse([]byte(dslYaml))
	if err != nil {
		return resolved, nil, err
	}
	strategy.Market = resolved.Market
	program, err := dsl.Compile(strategy, resolved.ViewColumns)
	if err != nil {
		return resolved, nil, err
	}
	resolved.PreviousFactors = previousFactors(resolved.Columns, program)
	return resolved, program, nil
}

// ParseResolved 解析保存的 resolved_json。
func ParseResolved(raw []byte) (Resolved, error) {
	var resolved Resolved
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return Resolved{}, &describedError{message: "会话快照的 resolved_json 无法解析", cause: err}
	}
	if resolved.Market.IsZero() || len(resolved.Tasks) == 0 || resolved.Bar == "" || resolved.Calendar == "" {
		return Resolved{}, errors.New("resolved_json 缺少 market、tasks、bar 或 calendar")
	}
	if resolved.Unified() {
		if len(resolved.Tasks) != 1 {
			return Resolved{}, errors.New("resolved_json 的统一数据集任务不能与其他任务并存")
		}
	} else if got := resolved.Markets(); len(got) != len(resolved.Tasks) || len(got) != len(resolved.Market.Markets()) {
		return Resolved{}, errors.New("resolved_json 的 tasks 与 market 不一致")
	}
	for market, task := range resolved.Tasks {
		if task.ViewID == "" || task.FactorTaskID == "" {
			return Resolved{}, fmt.Errorf("resolved_json 的 %s 任务缺少 view_id 或 factor_task_id", market)
		}
	}
	if resolved.Columns == nil {
		resolved.Columns = map[string]ColumnBinding{}
	}
	if resolved.Factors == nil {
		resolved.Factors = map[string]string{}
	}
	return resolved, nil
}

// ReadinessBinding 把解析结果转为就绪判定需要的部分。
func (r Resolved) ReadinessBinding() readiness.Binding {
	factors := make(map[string]string, len(r.Factors))
	for id, hash := range r.Factors {
		factors[id] = hash
	}
	return readiness.Binding{Factors: factors, PreviousFactors: append([]string(nil), r.PreviousFactors...)}
}

// ReferencedTags 返回 DSL 引用的全部标签（去重、排序），回放按它预取标签成员。
func ReferencedTags(strategy dsl.Strategy) []string { return referencedTags(strategy) }
