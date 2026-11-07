package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// 本文件是策略输入层的对照测试（执行计划 S1.8）：同样的行，分别放进两个模拟 View（方式 A：现货、永续各一个任务）和一个带两个
// series_tag 的模拟 View（方式 B：统一数据集上的一个任务），走同一条路径——就绪判断（按序列拆成各市场的结论）→ 汇合合并
// （checkReady）→ 输入装配（LoadBar）→ 引擎求值——得到的结论必须一致。

const (
	ucSpotTag = "venue:binance|market:spot|source:spot_http"
	ucSwapTag = "venue:binance|market:swap|source:swap_http"
)

var ucBar = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

// ucStorage 是内存里的 Storage 与 Factor 替身。
type ucStorage struct {
	views     map[string]input.ViewInfo
	datasets  map[string]input.DatasetInfo
	subjects  map[string][]input.Subject
	tags      map[string][]string
	tagMarket map[string]string
	rows      func(query input.Query) []input.Row
	queries   []input.Query
}

func (s *ucStorage) GetView(_ context.Context, _, viewID string) (input.ViewInfo, error) {
	view, ok := s.views[viewID]
	if !ok {
		return input.ViewInfo{}, fmt.Errorf("View %s 不存在", viewID)
	}
	return view, nil
}

func (s *ucStorage) ViewGeneration(ctx context.Context, spaceID, viewID string) (string, error) {
	view, err := s.GetView(ctx, spaceID, viewID)
	return view.Generation, err
}

func (s *ucStorage) ViewCoverage(context.Context, string, input.ViewInfo, bool) (input.Coverage, error) {
	return input.Coverage{IndexedFrom: ucBar.Add(-500 * time.Hour), IndexedTo: ucBar, SeriesBars: 1000}, nil
}

func (s *ucStorage) GetDataset(_ context.Context, _, datasetID string) (input.DatasetInfo, error) {
	dataset, ok := s.datasets[datasetID]
	if !ok {
		return input.DatasetInfo{}, fmt.Errorf("数据集 %s 不存在", datasetID)
	}
	return dataset, nil
}

func (s *ucStorage) ListDatasetSubjects(_ context.Context, _, datasetID string) ([]input.Subject, error) {
	return append([]input.Subject(nil), s.subjects[datasetID]...), nil
}

func (s *ucStorage) ListTagMembers(_ context.Context, _, tagID string) ([]string, error) {
	return append([]string(nil), s.tags[tagID]...), nil
}

func (s *ucStorage) GetTag(_ context.Context, _, tagID string) (input.TagInfo, error) {
	return input.TagInfo{MarketType: s.tagMarket[tagID]}, nil
}

func (s *ucStorage) QueryRows(_ context.Context, _ string, query input.Query) ([]input.Row, uint64, error) {
	s.queries = append(s.queries, query)
	wanted := map[string]string{}
	for _, subject := range query.Subjects {
		wanted[subject.SubjectID] = subject.SeriesTag
	}
	var out []input.Row
	for _, row := range s.rows(query) {
		tag, ok := wanted[row.SubjectID]
		if !ok || (tag != "" && tag != row.SeriesTag) {
			continue // 与 Storage 一致：selector 带 tag 只取那条序列，不带取全部序列
		}
		out = append(out, row)
	}
	return out, 1, nil
}

func (s *ucStorage) GetFactor(_ context.Context, factorID string) (input.FactorInfo, error) {
	return input.FactorInfo{FactorID: factorID, DefinitionHash: "h1"}, nil
}

// ucWorld 描述一期里两个市场的全部情况；方式 A、B 各用它生成自己的夹具。
type ucWorld struct {
	dsl string
	// members 是各市场的活跃标的（标签成员）；rows 是各市场本期的因子值 m（失败的标的没有行）。
	members map[string][]string
	rows    map[string]map[string]float64
	// failed 是各市场单个标的的因子失败；skipped 是因子在该市场整体被跳过（例如严格截面因子缺了一个币）。
	failed  map[string][]string
	skipped map[string]bool
	// history 是各市场在年龄探针窗口里有历史行的标的。
	history map[string][]string
	// extra 是只出现在方式 B 里的标的：它们在市场标签里是活跃成员，但因子没有期望这条序列（刚新上标签、universe_series 里还没有，
	// 或已停更而标签没移除），所以既没有行也不在失败名单里；方式 A 的数据集里没有它们。
	extra map[string][]string
	// state 是上一期的引擎状态（持有的标的与目标）。
	state engine.State
}

func (w ucWorld) tagOf(market string) string {
	if market == dsl.MarketSwap {
		return ucSwapTag
	}
	return ucSpotTag
}

func (w ucWorld) rowsFor(query input.Query, markets ...string) []input.Row {
	var out []input.Row
	probe := len(query.Columns) == 1 && query.Columns[0] == "close"
	for _, market := range markets {
		if probe {
			for _, id := range w.history[market] {
				out = append(out, input.Row{SubjectID: id, SeriesTag: w.tagOf(market), DataTime: query.Start, Values: map[string]float64{"close": 1}})
			}
			continue
		}
		for id, value := range w.rows[market] {
			out = append(out, input.Row{SubjectID: id, SeriesTag: w.tagOf(market), DataTime: ucBar, Values: map[string]float64{"m": value, "close": 1}})
		}
	}
	return out
}

func ucColumns() []input.ViewColumn {
	m := input.ViewColumn{Name: "m", Attributes: map[string]string{"column_kind": "output", "member_key": "f", "factor_id": "f", "member_kind": "fixed", "output_name": "m"}}
	return []input.ViewColumn{{Name: "close", Attributes: map[string]string{}}, m}
}

// ucSubjects 把标的列表变成数据集绑定：inactive 里的标的已停用。
func ucSubjects(tag string, active, inactive []string) []input.Subject {
	var out []input.Subject
	for _, id := range active {
		out = append(out, input.Subject{SubjectID: id, SeriesTag: tag, Active: true})
	}
	for _, id := range inactive {
		out = append(out, input.Subject{SubjectID: id, SeriesTag: tag, Active: false})
	}
	return out
}

func union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

func ucState(status string, failedSubjects []string, failedSeries []*factorpb.SeriesRef, skippedTags []string) *factorpb.FactorPeriodState {
	return &factorpb.FactorPeriodState{MemberKey: "f", MemberKind: "fixed", FactorId: "f", Status: status, DefinitionHash: "h1",
		FailedSubjects: failedSubjects, FailedSeries: failedSeries, SkippedSeriesTags: skippedTags}
}

func ucEvent(viewID string, universe []string, state *factorpb.FactorPeriodState, shared []*factorpb.SeriesRef) *storagepb.ViewDataReady {
	raw, err := proto.Marshal(&factorpb.PeriodDetail{Factors: []*factorpb.FactorPeriodState{state}, SharedFailedSeries: shared})
	if err != nil {
		panic(err)
	}
	status := "complete"
	if state.GetStatus() != "complete" {
		status = "degraded"
	}
	if universe == nil {
		universe = []string{}
	}
	return &storagepb.ViewDataReady{ViewId: viewID, Frequency: "1h", PeriodTime: ucBar.Unix(), Status: status, UniverseSubjectIds: universe, DetailType: readiness.PeriodDetailType, Detail: raw}
}

// build 生成方式 A（unified=false）或方式 B 的 Storage 替身、任务与各任务键的就绪事件。
func (w ucWorld) build(t *testing.T, unified bool) (*ucStorage, []string, map[string]*storagepb.ViewDataReady) {
	t.Helper()
	mode := parseMode(t, w.dsl)
	client := &ucStorage{views: map[string]input.ViewInfo{}, datasets: map[string]input.DatasetInfo{}, subjects: map[string][]input.Subject{}, tags: map[string][]string{},
		tagMarket: map[string]string{"binance_spot": "spot", "binance_swap": "swap"}}
	addView := func(task, dataset, source, tagID string, attrs map[string]string, tagIDs []string) {
		suffix := strings.TrimPrefix(task, "ftask_")
		view := "view_factor_" + suffix
		client.views[view] = input.ViewInfo{ViewID: view, DatasetID: dataset, Frequency: "1h", Status: "active", ActiveIndexID: "idx", Columns: ucColumns()}
		client.datasets[dataset] = input.DatasetInfo{DatasetID: dataset, Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{"source_dataset_id": source}, SubjectTags: tagIDs}
		client.datasets[source] = input.DatasetInfo{DatasetID: source, Status: "active", Frequency: "1h", Retention: "720h", Attributes: attrs, SubjectTags: tagIDs}
	}
	events := map[string]*storagepb.ViewDataReady{}
	universeOf := func(market string) []string { return w.members[market] }
	switch {
	case unified:
		addView("ftask_uni", "ds_uni", "src_uni", "", map[string]string{"market_type": "multi"}, []string{"binance_spot", "binance_swap"})
		all := union(w.members[dsl.MarketSpot], w.members[dsl.MarketSwap], w.extra[dsl.MarketSpot], w.extra[dsl.MarketSwap])
		// 数据集的绑定里每个标的只有一条；已下架的永续标的仍然绑定着（现货还在）。
		client.subjects["ds_uni"] = ucSubjects(ucSpotTag, all, nil)
		client.tags["binance_spot"] = union(w.members[dsl.MarketSpot], w.extra[dsl.MarketSpot])
		client.tags["binance_swap"] = union(w.members[dsl.MarketSwap], w.extra[dsl.MarketSwap])
		client.rows = func(query input.Query) []input.Row { return w.rowsFor(query, dsl.MarketSpot, dsl.MarketSwap) }
		// 因子明细：按标的折叠的 failed_subjects 是各市场失败的并集（模拟因子的实际输出），按序列的字段才是真相。
		var failedSeries []*factorpb.SeriesRef
		var folded, skippedTags []string
		for _, market := range []string{dsl.MarketSpot, dsl.MarketSwap} {
			for _, id := range w.failed[market] {
				failedSeries = append(failedSeries, &factorpb.SeriesRef{SubjectId: id, SeriesTag: w.tagOf(market)})
				folded = append(folded, id)
			}
			if w.skipped[market] {
				skippedTags = append(skippedTags, w.tagOf(market))
			}
		}
		status := "complete"
		switch {
		case len(skippedTags) == 2:
			status = "skipped"
		case len(failedSeries) > 0 || len(skippedTags) > 0:
			status = "degraded"
		}
		events[input.MarketMulti] = ucEvent("view_factor_uni", all, ucState(status, union(folded), failedSeries, skippedTags), nil)
		return client, []string{"ftask_uni"}, events
	}
	var tasks []string
	client.rows = func(query input.Query) []input.Row {
		var markets []string
		switch query.ViewID {
		case "view_factor_sp":
			markets = []string{dsl.MarketSpot}
		case "view_factor_sw":
			markets = []string{dsl.MarketSwap}
		}
		return w.rowsFor(query, markets...)
	}
	for _, market := range mode.Markets() {
		suffix, tagID := "sp", "binance_spot"
		if market == dsl.MarketSwap {
			suffix, tagID = "sw", "binance_swap"
		}
		task := "ftask_" + suffix
		tasks = append(tasks, task)
		addView(task, "ds_"+suffix, "src_"+suffix, tagID, map[string]string{}, []string{tagID})
		// 已下架的标的在方式 A 里是数据集里已停用的绑定。
		var inactive []string
		for _, id := range union(w.members[dsl.MarketSpot], w.members[dsl.MarketSwap]) {
			if !contains(w.members[market], id) {
				inactive = append(inactive, id)
			}
		}
		client.subjects["ds_"+suffix] = ucSubjects(w.tagOf(market), w.members[market], inactive)
		client.tags[tagID] = w.members[market]
		status := "complete"
		switch {
		case w.skipped[market]:
			status = "skipped"
		case len(w.failed[market]) > 0:
			status = "degraded"
		}
		events[market] = ucEvent("view_factor_"+suffix, universeOf(market), ucState(status, w.failed[market], nil, nil), nil)
	}
	return client, tasks, events
}

func contains(list []string, id string) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}

func parseMode(t *testing.T, text string) dsl.Market {
	t.Helper()
	strategy, err := dsl.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return strategy.Market
}

// run 走一遍就绪判断 → checkReady → LoadBar → 引擎求值，返回决策与装配的帧。
func (w ucWorld) run(t *testing.T, unified bool) engine.Decision {
	t.Helper()
	ctx := context.Background()
	client, tasks, events := w.build(t, unified)
	strategy, err := dsl.Parse([]byte(w.dsl))
	if err != nil {
		t.Fatal(err)
	}
	resolved, program, err := input.Resolve(ctx, client, "space", tasks, strategy)
	if err != nil {
		t.Fatalf("解析绑定失败（unified=%v）：%v", unified, err)
	}
	if resolved.Unified() != unified {
		t.Fatalf("绑定方式不符：%+v", resolved)
	}
	p := &period{runtime: &runtime{resolved: resolved, program: program}, events: events, input: store.InputRecord{Views: map[string]store.ViewInput{}}}
	ready := (&Handler{}).checkReady(p, nil)
	if ready.Reason != "" {
		return engine.Decision{Status: engine.StatusSkipped, SkipReason: ready.Reason}
	}
	loaded, err := input.Loader{Client: client}.LoadBar(ctx, "space", resolved, program, input.Bar{BarStart: ucBar, EventUniverse: p.eventUniverses(resolved), Readiness: ready.Results, Unavailable: ready.Unavailable, Held: w.state.HeldMarkets()})
	if err != nil {
		var skip *input.SkipError
		if asSkip(err, &skip) {
			return engine.Decision{Status: engine.StatusSkipped, SkipReason: skip.Reason, Summary: engine.Summary{Notes: []string{skip.Detail}}}
		}
		t.Fatalf("装配失败（unified=%v）：%v", unified, err)
	}
	decision, err := engine.Evaluate(program, loaded.Frame, w.state)
	if err != nil {
		t.Fatalf("求值失败（unified=%v）：%v", unified, err)
	}
	return decision
}

func asSkip(err error, target **input.SkipError) bool {
	skip, ok := err.(*input.SkipError)
	if ok {
		*target = skip
	}
	return ok
}

// summarize 把决策里要比较的部分整理成稳定的文本：状态、跳过原因、目标（含保持标记）与状态 JSON。
func summarize(t *testing.T, decision engine.Decision) (string, map[string]string) {
	t.Helper()
	targets := map[string]string{}
	for _, target := range decision.Targets {
		key := engine.TargetKey(target.InstrumentID, target.Market)
		targets[key] = target.Weight.String()
		if target.Hold {
			targets[key] += "(hold)"
		}
	}
	raw, err := json.Marshal(decision.State)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s/%s targets=%v state=%s", decision.Status, decision.SkipReason, targets, raw), targets
}

const ucDSLTemplate = `name: compare
market: %s
%srules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 1}
portfolio: {max_missing: 1}
`

func ucDSL(mode string, minAge bool) string {
	universe := ""
	if minAge {
		universe = "universe: {min_age_bars: 24}\n"
	}
	return fmt.Sprintf(ucDSLTemplate, mode, universe)
}

// ucBase 是各场景共用的基础数据：现货有 A、B、C、X，永续有 B、C、D、X；X 在永续上的因子值最大。
func ucBase(mode string) ucWorld {
	return ucWorld{
		dsl:     ucDSL(mode, false),
		members: map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"B", "C", "D", "X"}},
		rows: map[string]map[string]float64{
			"spot": {"A": 1, "B": 2, "C": 3, "X": 4},
			"swap": {"B": 5, "C": 6, "D": 7, "X": 8},
		},
		failed: map[string][]string{}, skipped: map[string]bool{}, history: map[string][]string{},
	}
}

// requireSame 断言方式 A、B 得到同样的决策，并返回 B 的目标供场景再断言具体内容（两边同样的错也会通过对照，所以场景自己还要核对结果）。
func requireSame(t *testing.T, world ucWorld) (engine.Decision, map[string]string) {
	t.Helper()
	a, b := world.run(t, false), world.run(t, true)
	textA, _ := summarize(t, a)
	textB, targets := summarize(t, b)
	if textA != textB {
		t.Fatalf("方式 A 与方式 B 的结论不一致：\n A：%s\n B：%s", textA, textB)
	}
	return b, targets
}

// 统一数据集上的五种市场模式：同样的行，方式 A 与方式 B 的目标一致。
func TestUnifiedMatchesDualTasksInAllFiveModes(t *testing.T) {
	cases := []struct {
		mode string
		want map[string]string
	}{
		{"{select: spot, prefer: spot}", map[string]string{"X|spot": "0.5", "C|spot": "0.5"}},
		{"{select: swap, prefer: swap}", map[string]string{"X|swap": "0.5", "D|swap": "0.5"}},
		{"{select: both, prefer: spot}", map[string]string{"X|spot": "0.5", "D|swap": "0.5"}},
		{"{select: both, prefer: swap}", map[string]string{"X|swap": "0.5", "D|swap": "0.5"}},
		{"{select: spot, prefer: swap}", map[string]string{"X|swap": "0.5", "C|swap": "0.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			_, targets := requireSame(t, ucBase(tc.mode))
			if !reflect.DeepEqual(targets, tc.want) {
				t.Fatalf("目标不符：got %v want %v", targets, tc.want)
			}
		})
	}
}

// 新上永续的币（年龄）：X 在现货上有足够的历史、永续刚上线没有历史：永续不能借用现货的历史通过年龄检查，X 只用现货的行。
func TestUnifiedMatchesDualTasksForNewSwapListing(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.dsl = ucDSL("{select: both, prefer: swap}", true)
	world.history = map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"B", "C", "D"}}
	_, targets := requireSame(t, world)
	if _, onSwap := targets["X|swap"]; onSwap {
		t.Fatalf("永续上还没有足够历史的 X 不应在永续下单：%v", targets)
	}
	if !reflect.DeepEqual(targets, map[string]string{"D|swap": "0.5", "C|swap": "0.5"}) {
		t.Fatalf("目标不符：%v", targets)
	}
}

// 永续下架而现货还在（活跃）：永续上的 Y 不可用，现货照常；Y 在永续上还有一行残留数据，也不能用。
func TestUnifiedMatchesDualTasksForDelistedSwap(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.members = map[string][]string{"spot": {"A", "B", "C", "X", "Y"}, "swap": {"B", "C", "D", "X"}}
	world.rows["spot"]["Y"] = 9
	world.rows["swap"]["Y"] = 10
	_, targets := requireSame(t, world)
	if !reflect.DeepEqual(targets, map[string]string{"Y|spot": "0.5", "X|swap": "0.5"}) {
		t.Fatalf("下架永续的 Y 应只在现货上可用：%v", targets)
	}
}

// 单个币永续失败（失败隔离）：C 在永续上失败，现货上的 C 不受影响；按标的折叠的失败名单里有 C，但不能让现货的 C 一起失败。
func TestUnifiedMatchesDualTasksForSingleSwapFailure(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	delete(world.rows["swap"], "C")
	world.failed["swap"] = []string{"C"}
	world.rows["spot"]["C"] = 100
	_, targets := requireSame(t, world)
	if !reflect.DeepEqual(targets, map[string]string{"C|spot": "0.5", "X|swap": "0.5"}) {
		t.Fatalf("现货上的 C 应照常可用：%v", targets)
	}
}

// 严格截面因子在永续上缺一个币：永续本期不可用，现货照常；没有持仓时只在现货下单。
func TestUnifiedMatchesDualTasksWhenSwapSkipped(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	delete(world.rows["swap"], "D")
	world.failed["swap"] = []string{"D"}
	world.skipped["swap"] = true
	_, targets := requireSame(t, world)
	if !reflect.DeepEqual(targets, map[string]string{"X|spot": "0.5", "C|spot": "0.5"}) {
		t.Fatalf("永续不可用时应只在现货下单：%v", targets)
	}
}

// 永续整个不可用而策略持有永续：持仓冻结，不被卖出；新开仓只用现货。
func TestUnifiedMatchesDualTasksFreezesHeldSwapWhenSwapUnavailable(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.skipped["swap"] = true
	world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"D"}}}, Targets: map[string]string{"D|swap": "0.5", "X|swap": "0.5"}}
	_, targets := requireSame(t, world)
	for _, key := range []string{"D|swap", "X|swap"} {
		if !strings.HasSuffix(targets[key], "(hold)") {
			t.Fatalf("永续上的持仓 %s 应冻结（保持）：%v", key, targets)
		}
	}
}

// 全部市场都不可用：整期按原因跳过，与方式 A 一致。
func TestUnifiedMatchesDualTasksWhenEveryMarketSkipped(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.skipped = map[string]bool{"spot": true, "swap": true}
	decision, _ := requireSame(t, world)
	if decision.Status != engine.StatusSkipped || decision.SkipReason != readiness.ReasonFactorSkipped {
		t.Fatalf("全部市场都不可用应整期跳过：%+v", decision)
	}
}

// 统一数据集上的实例只有一个 View 和一条事件：到达即求值（不走汇合、不写到达记录），事件按任务键 multi 路由到实例。
func TestHandleEvaluatesUnifiedInstanceOnArrival(t *testing.T) {
	resolved := input.Resolved{Market: dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, Tasks: map[string]input.TaskBinding{
		input.MarketMulti: {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}},
		Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}},
		Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "m"}}
	h := newHarnessWith(t, dualRankDSL, nil, resolved)
	h.frame(1, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 1}})
	if err := h.deliver(1); err != nil {
		t.Fatal(err)
	}
	result := h.resultAt(1)
	if result.Status != engine.StatusOK {
		t.Fatalf("统一数据集上的实例到达即应求值：%+v", result)
	}
	// 一个 View 的一条事件不写汇合用的到达记录。
	if arrivals, _ := h.repo.ReadyEventsAt(context.Background(), "i1", h.session, barStart(1).Add(time.Hour)); len(arrivals) != 0 {
		t.Fatalf("统一数据集不走汇合，不应写到达记录：%d 条", len(arrivals))
	}
	// 事件所在的 View 不在绑定里时丢弃。
	message, payload := h.event(2, map[string]string{"f": "h1"})
	payload.ViewId = "view_other"
	if err := h.handler.Handle(context.Background(), message, payload); err != nil {
		t.Fatal(err)
	}
	h.noResultAt(2)
}

// 年龄缓存为空（新启用、索引换代）时，永续在探针窗口里整体没有行而现货有：方式 B 的确认查询只看永续自己的序列，
// 与方式 A 一样整期 history_insufficient，持仓不动，不能把永续全部判成新上市而卖掉持有的永续。
func TestUnifiedMatchesDualTasksWhenSwapHistoryHasGap(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.dsl = ucDSL("{select: both, prefer: swap}", true)
	world.history = map[string][]string{"spot": {"A", "B", "C", "X"}}
	world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"D", "X"}}}, Targets: map[string]string{"D|swap": "0.5", "X|swap": "0.5"}}
	decision, _ := requireSame(t, world)
	if decision.Status != engine.StatusSkipped || decision.SkipReason != input.SkipHistoryInsufficient {
		t.Fatalf("永续整段历史缺口应整期 history_insufficient：%+v", decision)
	}
}

// 持仓的标的在永续上的年龄探针没通过（探针窗口有缺口）时豁免：年龄剔除只拦新进入的标的，持仓不能因为行被删而被卖出。
func TestUnifiedMatchesDualTasksKeepsHeldSwapWhenAgeProbeFails(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.dsl = ucDSL("{select: both, prefer: swap}", true)
	// 探针窗口里永续只有 D 有行：持有的 X 的永续没通过年龄；现货的历史都有。
	world.history = map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"D"}}
	world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"D", "X"}}}, Targets: map[string]string{"D|swap": "0.5", "X|swap": "0.5"}}
	_, targets := requireSame(t, world)
	if !reflect.DeepEqual(targets, map[string]string{"D|swap": "0.5", "X|swap": "0.5"}) {
		t.Fatalf("持有的 X 的永续应豁免年龄剔除、继续持有：%v", targets)
	}
}

// 时序因子在永续整体晚到：永续所有序列都在失败名单里（不是被跳过）。某个因子在永续基础集合的每个币上都失败，按永续不可用处理：
// 持有的永续冻结，不会被“缺数比例没超过 max_missing”放过而卖成现货（方式 A、B 一致，三种含永续下单的模式都一样）。
func TestUnifiedMatchesDualTasksFreezesHeldSwapWhenSwapWhollyFailed(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}", "{select: spot, prefer: swap}"} {
		t.Run(mode, func(t *testing.T) {
			world := ucBase(mode)
			world.failed["swap"] = world.members["swap"]
			world.rows["swap"] = map[string]float64{}
			// 持有的永续：D 只在永续上市，选币只看现货时它不会是持仓。
			held, keys := []string{"D", "X"}, []string{"D|swap", "X|swap"}
			if mode == "{select: spot, prefer: swap}" {
				held, keys = []string{"X"}, []string{"X|swap"}
			}
			world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: held}}, Targets: map[string]string{}}
			for _, key := range keys {
				world.state.Targets[key] = "0.5"
			}
			_, targets := requireSame(t, world)
			for _, key := range keys {
				if !strings.HasSuffix(targets[key], "(hold)") {
					t.Fatalf("永续整体失败时持仓 %s 应冻结：%v", key, targets)
				}
			}
		})
	}
}

// 选币市场整体失败没有候选池：整期按数据类原因跳过（与因子被跳过一致）；只有下单市场整体失败时选币照常。
func TestUnifiedMatchesDualTasksWhenSelectionMarketWhollyFailed(t *testing.T) {
	world := ucBase("{select: spot, prefer: swap}")
	world.failed["spot"] = world.members["spot"]
	world.rows["spot"] = map[string]float64{}
	decision, _ := requireSame(t, world)
	if decision.Status != engine.StatusSkipped || decision.SkipReason != input.SkipMarketFailed {
		t.Fatalf("选币市场整体失败应整期按 market_failed 跳过：%+v", decision)
	}
}

// 永续整体晚到、四个永续币全部失败，但永续标签里还有一个因子没有期望这条序列的币 Y（现货也有它）：方式 B 的永续基础集合里多出的
// Y 既没有行也不在失败名单里，“每个币都失败”的判断只看有行或在失败名单里的标的，与方式 A 一致：持有的永续冻结，不卖成现货。
func TestUnifiedMatchesDualTasksWhenSwapWhollyFailedWithUnexpectedSeries(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}", "{select: spot, prefer: swap}"} {
		t.Run(mode, func(t *testing.T) {
			world := ucBase(mode)
			world.members["spot"] = []string{"A", "B", "C", "X", "Y"}
			world.rows["spot"]["Y"] = 9
			world.extra = map[string][]string{"swap": {"Y"}}
			world.failed["swap"] = world.members["swap"]
			world.rows["swap"] = map[string]float64{}
			held, keys := []string{"D", "X"}, []string{"D|swap", "X|swap"}
			if mode == "{select: spot, prefer: swap}" {
				held, keys = []string{"X"}, []string{"X|swap"}
			}
			world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: held}}, Targets: map[string]string{}}
			for _, key := range keys {
				world.state.Targets[key] = "0.5"
			}
			_, targets := requireSame(t, world)
			for _, key := range keys {
				if !strings.HasSuffix(targets[key], "(hold)") {
					t.Fatalf("永续整体失败时持仓 %s 应冻结：%v", key, targets)
				}
			}
		})
	}
}

// 部分失败的边界：永续 4 个里 3 个失败，超过 max_missing（0.2）就按永续不可用处理（与单市场守门同口径）：多市场策略里持有的永续冻结，
// 不会因为没持仓的币退回现货的行、缺数分母被摊薄而被卖出；纯永续（单市场）仍然是整期 too_many_missing。
func TestUnifiedMatchesDualTasksWhenMostSwapSeriesFailed(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}"} {
		t.Run(mode, func(t *testing.T) {
			world := ucBase(mode)
			world.dsl = strings.Replace(world.dsl, "max_missing: 1", "max_missing: 0.2", 1)
			world.failed["swap"] = []string{"B", "C", "D"}
			world.rows["swap"] = map[string]float64{"X": 8}
			world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"D", "X"}}}, Targets: map[string]string{"D|swap": "0.5", "X|swap": "0.5"}}
			// 永续不可用后冻结的持仓仍计入缺数，比例超过 max_missing：整期 too_many_missing，持仓不动，不会卖出。
			decision, _ := requireSame(t, world)
			if decision.Status != engine.StatusSkipped || decision.SkipReason != engine.SkipTooManyMissing {
				t.Fatalf("永续大面积失败时应整期跳过、持仓不动：%+v", decision)
			}
		})
	}
	single := ucBase("{select: swap, prefer: swap}")
	single.dsl = strings.Replace(single.dsl, "max_missing: 1", "max_missing: 0.2", 1)
	single.failed["swap"] = []string{"B", "C", "D"}
	single.rows["swap"] = map[string]float64{"X": 8}
	decision, _ := requireSame(t, single)
	if decision.Status != engine.StatusSkipped || decision.SkipReason != engine.SkipTooManyMissing {
		t.Fatalf("纯永续（单市场）大面积失败仍整期 too_many_missing：%+v", decision)
	}
}

// ucDSLWith 是带 universe 片段与 max_missing 的 rank 策略。
func ucDSLWith(mode, universe, maxMissing string) string {
	return "name: compare\nmarket: " + mode + "\n" + universe + "rules:\n  - id: r\n    type: rank\n    score: \"m\"\n    select: {top: 2}\n    weight: {total: 1}\nportfolio: {max_missing: " + maxMissing + "}\n"
}

// ucHeld 让策略上期持有永续：D、X（选币只看现货时 D 不会是持仓，只有 X）；返回持有的目标键。
func ucHeld(world *ucWorld, mode string) []string {
	held, keys := []string{"D", "X"}, []string{"D|swap", "X|swap"}
	if mode == "{select: spot, prefer: swap}" {
		held, keys = []string{"X"}, []string{"X|swap"}
	}
	world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: held}}, Targets: map[string]string{}}
	for _, key := range keys {
		world.state.Targets[key] = "0.5"
	}
	return keys
}

// 策略的范围被收窄时（universe.exclude、固定池），范围之外的币失败不能让整个永续被判不可用：整体失败的统计只看本次实际读行的标的，
// 与单市场守门只看规则的预期集合一致。
func TestUnifiedMatchesDualTasksIgnoresFailuresOutsideThePool(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}", "{select: spot, prefer: swap}", "{select: swap, prefer: swap}"} {
		t.Run("排除名单"+mode, func(t *testing.T) {
			world := ucBase(mode)
			world.dsl = ucDSLWith(mode, "universe: {exclude: [P, Q]}\n", "0.2")
			world.members["swap"] = []string{"B", "C", "D", "X", "P", "Q"}
			world.failed["swap"] = []string{"P", "Q"}
			keys := ucHeld(&world, mode)
			decision, targets := requireSame(t, world)
			if decision.Status != engine.StatusOK {
				t.Fatalf("排除名单之外的失败不应影响池内：%+v", decision)
			}
			for _, key := range keys {
				if strings.HasSuffix(targets[key], "(hold)") {
					t.Fatalf("%s 不应被冻结：%v", key, targets)
				}
			}
		})
	}
	signalPool := "name: compare\nmarket: {select: both, prefer: swap}\nrules:\n  - id: r\n    type: signal\n    pool: [D, X]\n    entry: \"m > 0\"\n    exit: \"m < 0\"\n    weight: {total: 1}\nportfolio: {max_missing: 0.2}\n"
	for _, failed := range [][]string{{"B"}, {"B", "C"}} {
		t.Run("固定池"+strings.Join(failed, ""), func(t *testing.T) {
			world := ucBase("{select: both, prefer: swap}")
			world.dsl = signalPool
			world.failed["swap"] = failed
			for _, id := range failed {
				delete(world.rows["swap"], id)
			}
			ucHeld(&world, "{select: both, prefer: swap}")
			decision, targets := requireSame(t, world)
			if decision.Status != engine.StatusOK || targets["D|swap"] != "0.5" || targets["X|swap"] != "0.5" {
				t.Fatalf("固定池之外的失败不应冻结池里的持仓或让整期跳过：%+v %v", decision, targets)
			}
		})
	}
}

// 年龄会剔除的新上市币（窗口里没有历史）即使因子失败，也不计入整体失败的比例：与单市场守门“预期集合减年龄剔除”的分母一致。
func TestUnifiedMatchesDualTasksIgnoresFailuresOfAgedOutListings(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: swap, prefer: swap}"} {
		t.Run(mode, func(t *testing.T) {
			world := ucBase(mode)
			world.dsl = ucDSLWith(mode, "universe: {min_age_bars: 24}\n", "0.2")
			world.members["swap"] = []string{"B", "C", "D", "X", "N1", "N2"}
			world.failed["swap"] = []string{"N1", "N2"}
			world.history = map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"B", "C", "D", "X"}}
			ucHeld(&world, mode)
			decision, _ := requireSame(t, world)
			if decision.Status != engine.StatusOK {
				t.Fatalf("年龄剔除的新上市币失败不应让整期跳过或冻结：%+v", decision)
			}
		})
	}
}

// 失败占比的边界：严格大于 max_missing 才算超过（与守门一致）：1/4 对 0.25 仍可用，对 0.24 不可用；max_missing 为 1 时只有全部失败才不可用。
func TestUnifiedMatchesDualTasksMaxMissingBoundaries(t *testing.T) {
	cases := []struct {
		name, maxMissing string
		failed           []string
		unavailable      bool
	}{
		{"0 时一个失败即不可用", "0", []string{"C"}, true},
		{"0 时没有失败", "0", nil, false},
		{"1 时三个失败仍可用", "1", []string{"B", "C", "D"}, false},
		{"1 时全部失败不可用", "1", []string{"B", "C", "D", "X"}, true},
		{"0.25 时 1/4 恰好等于上限，仍可用", "0.25", []string{"C"}, false},
		{"0.24 时 1/4 略超，不可用", "0.24", []string{"C"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode := "{select: both, prefer: swap}"
			world := ucBase(mode)
			world.dsl = ucDSLWith(mode, "", tc.maxMissing)
			world.failed["swap"] = tc.failed
			for _, id := range tc.failed {
				delete(world.rows["swap"], id)
			}
			ucHeld(&world, mode)
			decision, targets := requireSame(t, world)
			// 不可用时冻结的持仓计入缺数：要么保持（hold），要么整期跳过；可用时 X 照常带目标、不是 hold。
			frozen := strings.HasSuffix(targets["X|swap"], "(hold)") || decision.Status == engine.StatusSkipped
			if frozen != tc.unavailable {
				t.Fatalf("永续不可用=%v，期望 %v：%+v %v", frozen, tc.unavailable, decision, targets)
			}
		})
	}
}

// 一个市场的因子被跳过（汇合时判不可用）、另一个市场整体失败：整期 market_failed，说明里两个市场各自的原因都有，不是只取第一个。
func TestUnifiedSkipDetailListsEveryUnavailableMarket(t *testing.T) {
	world := ucBase("{select: both, prefer: swap}")
	world.skipped["spot"] = true
	world.failed["swap"] = world.members["swap"]
	world.rows["swap"] = map[string]float64{}
	for _, unified := range []bool{false, true} {
		decision := world.run(t, unified)
		if decision.SkipReason != input.SkipMarketFailed {
			t.Fatalf("unified=%v：应整期 market_failed：%+v", unified, decision)
		}
		detail := strings.Join(decision.Summary.Notes, "")
		if !strings.Contains(detail, "spot市场：") || !strings.Contains(detail, "swap市场：") {
			t.Fatalf("unified=%v：说明里应有两个市场各自的原因：%q", unified, detail)
		}
	}
}

// 整体失败的市场不做年龄探针（先判不可用、持仓冻结），而只是部分失败的市场照常探针：永续窗口里没有历史时仍 history_insufficient。
func TestUnifiedMatchesDualTasksSkipsProbeForWhollyFailedMarket(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}", "{select: spot, prefer: swap}"} {
		t.Run(mode, func(t *testing.T) {
			world := ucBase(mode)
			world.dsl = ucDSLWith(mode, "universe: {min_age_bars: 24}\n", "1")
			world.history = map[string][]string{"spot": {"A", "B", "C", "X"}}
			world.failed["swap"] = world.members["swap"]
			world.rows["swap"] = map[string]float64{}
			keys := ucHeld(&world, mode)
			_, targets := requireSame(t, world)
			for _, key := range keys {
				if !strings.HasSuffix(targets[key], "(hold)") {
					t.Fatalf("永续整体失败应先判不可用、持仓冻结（不探针）：%v", targets)
				}
			}
			world.failed["swap"] = []string{"B"}
			world.rows["swap"] = map[string]float64{"C": 6, "D": 7, "X": 8}
			decision, _ := requireSame(t, world)
			if decision.SkipReason != input.SkipHistoryInsufficient {
				t.Fatalf("永续只是部分失败时仍要探针：%+v", decision)
			}
		})
	}
}

// ucWideSpot 是现货 11 个币（永续的 B、C 也在其中，D、X 只在永续），持有 D|swap、X|swap 各 0.5：现货的币多，才会摊薄缺数比例。
func ucWideSpot(mode string, maxMissing string) ucWorld {
	spot := []string{"A", "B", "C", "E", "F", "G", "H", "I", "J", "K", "L"}
	spotRows := map[string]float64{}
	for i, id := range spot {
		spotRows[id] = float64(i + 1)
	}
	world := ucWorld{
		dsl:     ucDSLWith(mode, "universe: {min_age_bars: 24}\n", maxMissing),
		members: map[string][]string{"spot": spot, "swap": {"B", "C", "D", "X"}},
		rows:    map[string]map[string]float64{"spot": spotRows, "swap": {"B": 50, "C": 60, "D": 70, "X": 80}},
		failed:  map[string][]string{}, skipped: map[string]bool{}, history: map[string][]string{"spot": spot},
	}
	ucHeld(&world, mode)
	return world
}

// 持仓豁免年龄剔除：持有的 D、X 在永续探针窗口里没有行（年龄没通过），本期它们的永续因子失败。失败比例要把持仓算进去（单市场守门与
// 引擎都对持仓豁免年龄剔除）：永续不可用、持仓冻结（或整期 too_many_missing、持仓不动），不能被当成逐币失败而卖成现货。
func TestUnifiedMatchesDualTasksCountsHeldAgedOutFailures(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: swap}", "{select: both, prefer: spot}"} {
		for _, fails := range [][]string{{"D", "X"}, {"D"}, {"X"}} {
			t.Run(mode+strings.Join(fails, ""), func(t *testing.T) {
				world := ucWideSpot(mode, "0.2")
				world.history["swap"] = []string{"B", "C"}
				world.failed["swap"] = fails
				for _, id := range fails {
					delete(world.rows["swap"], id)
				}
				decision, targets := requireSame(t, world)
				// 永续不可用：持仓冻结（0.5 且带 hold）；或冻结的持仓占满预期集合，整期 too_many_missing、持仓不动。
				if decision.Status == engine.StatusSkipped {
					return
				}
				for _, key := range []string{"D|swap", "X|swap"} {
					if targets[key] != "0.5(hold)" {
						t.Fatalf("持有的 %s 应冻结（0.5 且 hold），被卖出或改仓了：%s %v", key, decision.Status, targets)
					}
				}
			})
		}
	}
}

// 反方向：max_missing 为 1、永续的 B、C 因子失败，持有的 D、X 数据正常但年龄没通过：持仓算进统计后永续不是“全部失败”，
// 不会因为只统计探针通过的 B、C 就把整个永续判不可用、冻结正常的持仓。
func TestUnifiedMatchesDualTasksHeldAgedOutHealthySeriesStayAvailable(t *testing.T) {
	mode := "{select: both, prefer: swap}"
	world := ucWideSpot(mode, "1")
	world.history["swap"] = []string{"B", "C"}
	world.failed["swap"] = []string{"B", "C"}
	delete(world.rows["swap"], "B")
	delete(world.rows["swap"], "C")
	_, targets := requireSame(t, world)
	for _, key := range []string{"D|swap", "X|swap"} {
		if targets[key] != "0.5" {
			t.Fatalf("数据正常的持仓 %s 不应被冻结，也不应被卖出：%v", key, targets)
		}
	}
}

// 清行是必需的：失败比例判永续不可用后，没持仓的 X 要退回现货的行；不清行的话它会取到不可用的永续行，记成 market_unavailable 缺数。
func TestUnifiedMatchesDualTasksClearsRowsOfUnavailableMarket(t *testing.T) {
	mode := "{select: both, prefer: swap}"
	world := ucBase(mode)
	world.state = engine.State{}
	world.dsl = ucDSLWith(mode, "", "0.2")
	world.failed["swap"] = []string{"B", "C", "D"}
	for _, id := range world.failed["swap"] {
		delete(world.rows["swap"], id)
	}
	world.rows["spot"]["X"] = 100
	decision, targets := requireSame(t, world)
	if decision.Status != engine.StatusOK || targets["X|spot"] == "" || targets["X|swap"] != "" {
		t.Fatalf("永续不可用时没持仓的 X 应退回现货的行：%+v %v", decision, targets)
	}
}

// 同一个只在永续上、没持仓的币在不同的“市场不可用”路径上口径一致：因子被跳过（不探针）与失败比例超限（探针之后）都按年龄剔除记，
// 不再因为不可用市场的探针结果让它在规则级通过年龄、被当作缺数。
func TestUnifiedUnavailablePathsTreatSwapOnlyListingTheSame(t *testing.T) {
	mode := "{select: both, prefer: swap}"
	base := func() ucWorld {
		world := ucBase(mode)
		world.state = engine.State{}
		world.dsl = ucDSLWith(mode, "universe: {min_age_bars: 24}\n", "0.1")
		world.history = map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"B", "C", "D", "X"}}
		return world
	}
	skipped := base()
	skipped.skipped["swap"] = true
	ratio := base()
	ratio.failed["swap"] = []string{"B", "C", "X"}
	for _, id := range ratio.failed["swap"] {
		delete(ratio.rows["swap"], id)
	}
	a, _ := requireSame(t, skipped)
	b, _ := requireSame(t, ratio)
	textA, _ := summarize(t, a)
	textB, _ := summarize(t, b)
	if textA != textB {
		t.Fatalf("两条不可用路径对只在永续上的 D 的口径应一致：\n因子被跳过：%s\n失败比例：%s", textA, textB)
	}
}

// ucShortDSL 是只做空的 rank 策略（空头一律永续），leverage 2。
func ucShortDSL(mode, maxMissing string) string {
	return "name: compare\nmarket: " + mode + "\nrules:\n  - id: r\n    type: rank\n    side: short\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 0.5}\nportfolio: {max_missing: " + maxMissing + ", leverage: 2}\n"
}

// ucSwapDownPaths 是永续本期不可用的三条路径：因子被跳过、全部失败、失败比例超限（4 个里 2 个失败，超过 max_missing 0.2）。
var ucSwapDownPaths = []struct {
	name, maxMissing string
	apply            func(*ucWorld)
}{
	{"因子被跳过", "1", func(w *ucWorld) { w.skipped["swap"] = true }},
	{"全部失败", "1", func(w *ucWorld) {
		w.failed["swap"] = w.members["swap"]
		w.rows["swap"] = map[string]float64{}
	}},
	{"失败比例超限", "0.2", func(w *ucWorld) {
		w.failed["swap"] = []string{"B", "D"}
		delete(w.rows["swap"], "B")
		delete(w.rows["swap"], "D")
	}},
}

// 持有永续空头、永续本期不可用时，空头冻结不动：三条不可用路径 × 三种含永续下单的多市场模式 × 方式 A、B，都必须正常求值、空头保持。
// 以前空头只取 prefer 加另一个市场的行（粘住只看正权重），永续不可用时退回现货的行，规则用现货因子值重新排名，在永续上平仓或改仓；
// 空头规则守门又因为预期集合只剩冻结的空头而整期 too_many_missing。
func TestUnifiedMatchesDualTasksFreezesHeldSwapShorts(t *testing.T) {
	for _, mode := range []string{"{select: both, prefer: spot}", "{select: both, prefer: swap}", "{select: spot, prefer: swap}"} {
		for _, path := range ucSwapDownPaths {
			t.Run(path.name+"/"+mode, func(t *testing.T) {
				world := ucBase(mode)
				world.dsl = ucShortDSL(mode, path.maxMissing)
				path.apply(&world)
				world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"C", "X"}}}, Targets: map[string]string{"C|swap": "-0.25", "X|swap": "-0.25"}}
				decision, targets := requireSame(t, world)
				if decision.Status != engine.StatusOK {
					t.Fatalf("永续不可用时空头规则不守门，应正常求值：%s %v", decision.SkipReason, decision.Summary.Notes)
				}
				if len(targets) != 2 || targets["C|swap"] != "-0.25(hold)" || targets["X|swap"] != "-0.25(hold)" {
					t.Fatalf("永续不可用时持有的空头应冻结，也不新开空头：%v", targets)
				}
			})
		}
	}
}

// 多空策略（both/spot，默认 max_missing 0.2），永续 4 个币里 B 失败（1/4 超过 0.2，永续本期不可用）：只是一部分市场不可用，不整期跳过（设计 3.2.1）。
// 没有空头持仓时多头照常在现货开仓；持有永续空头 X 时空头保持不动、多头照常——冻结的 X 不是多头规则的持仓，不计入多头规则守门的缺数。
func TestUnifiedLongShortKeepsLongLegWhenSwapUnavailable(t *testing.T) {
	mode := "{select: both, prefer: spot}"
	longShort := "name: compare\nmarket: " + mode + "\nrules:\n  - id: l\n    type: rank\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 0.5}\n  - id: s\n    type: rank\n    side: short\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 0.5}\nportfolio: {leverage: 2}\n"
	cases := []struct {
		name  string
		state engine.State
		want  map[string]string
	}{
		{"没有空头持仓", engine.State{}, map[string]string{"X|spot": "0.5"}},
		{"持有永续空头", engine.State{Rules: map[string]engine.RuleState{"s": {Held: []string{"X"}}}, Targets: map[string]string{"X|swap": "-0.5"}},
			map[string]string{"C|spot": "0.5", "X|swap": "-0.5(hold)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := ucBase(mode)
			world.dsl = longShort
			world.failed["swap"] = []string{"B"}
			delete(world.rows["swap"], "B")
			world.state = tc.state
			decision, targets := requireSame(t, world)
			if decision.Status != engine.StatusOK || !reflect.DeepEqual(targets, tc.want) {
				t.Fatalf("永续不可用时多头腿应照常调仓：%s/%s %v，期望 %v", decision.Status, decision.SkipReason, targets, tc.want)
			}
		})
	}
}
