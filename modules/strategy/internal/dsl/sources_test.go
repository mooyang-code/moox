package dsl

import (
	"reflect"
	"strings"
	"testing"
)

// sourcesDSL 构造一份 1h 主帧加 1d 来源 daily 的策略；rule 是规则里 id、type 之外的字段。
func sourcesDSL(rule string) string {
	return "name: x\nbar: 1h\nsources:\n  daily: {bar: 1d}\nrules:\n  - {id: a, type: rank, select: {top: 1}, weight: 1, " + rule + "}\n"
}

// S7 附加来源：来源名.列名 可以出现在 filter、score（含截面函数参数）、scale 里，Columns 带来源前缀，SourceColumns 按来源分组，
// MainColumns 只剩主帧的列。
func TestSourcesCompile(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(sourcesDSL(`filter: "daily.close > daily.ma__20", score: "rank(daily.mom__20) + close", scale: {expr: "timing_coef"}`)))
	if err != nil {
		t.Fatalf("Parse：%v", err)
	}
	if !reflect.DeepEqual(strategy.Sources, []Source{{Name: "daily", Bar: "1d"}}) || !strategy.HasSources() {
		t.Fatalf("sources=%+v", strategy.Sources)
	}
	referenced, err := ReferencedColumns(strategy)
	if err != nil {
		t.Fatalf("ReferencedColumns：%v", err)
	}
	if want := []string{"close", "daily.close", "daily.ma__20", "daily.mom__20", "timing_coef"}; !reflect.DeepEqual(referenced, want) {
		t.Fatalf("ReferencedColumns=%v，期望 %v", referenced, want)
	}
	program, err := Compile(strategy, []string{"close", "timing_coef", "daily.close", "daily.ma__20", "daily.mom__20"})
	if err != nil {
		t.Fatalf("Compile：%v", err)
	}
	if want := map[string][]string{"daily": {"close", "ma__20", "mom__20"}}; !reflect.DeepEqual(program.SourceColumns, want) {
		t.Fatalf("SourceColumns=%v", program.SourceColumns)
	}
	if want := []string{"close", "timing_coef"}; !reflect.DeepEqual(program.MainColumns(), want) {
		t.Fatalf("MainColumns=%v", program.MainColumns())
	}
	filter := program.Rules[0].Filter
	if !reflect.DeepEqual(filter.Sources, []string{"daily"}) {
		t.Fatalf("filter.Sources=%v", filter.Sources)
	}
	got, err := filter.Run(map[string]any{"daily": map[string]float64{"close": 2, "ma__20": 1}, "close": 1.0, "timing_coef": 1.0, "bars": []map[string]float64{}, "score": 0.0, "instrument_id": "A"})
	if err != nil || got != true {
		t.Fatalf("filter 求值=%v，%v", got, err)
	}
	// 截面函数参数里的来源列也要分析到：rank 的内层表达式带着来源名。
	score := program.Rules[0].Score
	if len(score.Normalizers) != 1 || !reflect.DeepEqual(score.Normalizers[0].Inner.Sources, []string{"daily"}) {
		t.Fatalf("score 的截面参数没有带上来源：%+v", score.Normalizers)
	}
}

// groupBySourceDSL 构造一份 1h 主帧加 1d 来源 daily 的 rank 策略：分数是 score，分组列是 by。
func groupBySourceDSL(score, by string) string {
	return "name: x\nbar: 1h\nsources:\n  daily: {bar: 1d}\nrules:\n  - {id: a, type: rank, score: \"" + score + "\", select: {top: 2, group: {by: " + by + ", max: 1}}, weight: 1}\n"
}

// select.group.by 写 来源名.列名：与表达式引用的来源列走同一条路——进 ReferencedColumns、Columns、SourceColumns，不进 MainColumns；
// 只有 group.by 引用某个来源时也算这个来源被引用；来源列不在绑定的来源 View 里时编译报列名。
func TestSourcesGroupByCompile(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(groupBySourceDSL("close", "daily.industry")))
	if err != nil {
		t.Fatalf("只有 group.by 引用来源时应通过保存校验：%v", err)
	}
	if by := strategy.Rules[0].Select.Group.By; by != "daily.industry" {
		t.Fatalf("group.by=%q", by)
	}
	referenced, err := ReferencedColumns(strategy)
	if err != nil || !reflect.DeepEqual(referenced, []string{"close", "daily.industry"}) {
		t.Fatalf("ReferencedColumns=%v，%v", referenced, err)
	}
	program, err := Compile(strategy, []string{"close", "daily.industry", "daily.close"})
	if err != nil {
		t.Fatalf("Compile：%v", err)
	}
	if want := map[string][]string{"daily": {"industry"}}; !reflect.DeepEqual(program.SourceColumns, want) {
		t.Fatalf("SourceColumns=%v", program.SourceColumns)
	}
	if !reflect.DeepEqual(program.MainColumns(), []string{"close"}) || !reflect.DeepEqual(program.Rules[0].Columns(), []string{"close", "daily.industry"}) {
		t.Fatalf("MainColumns=%v，规则列=%v", program.MainColumns(), program.Rules[0].Columns())
	}
	if _, err := Compile(strategy, []string{"close", "daily.close"}); err == nil || !strings.Contains(err.Error(), `"daily.industry"`) {
		t.Fatalf("来源列不存在时应报列名：%v", err)
	}
	// 与表达式里的来源列并存：同一来源的列合并去重。
	both, err := Parse([]byte(groupBySourceDSL("daily.close", "daily.industry")))
	if err != nil {
		t.Fatal(err)
	}
	program, err = Compile(both, []string{"close", "daily.industry", "daily.close"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"daily": {"close", "industry"}}; !reflect.DeepEqual(program.SourceColumns, want) {
		t.Fatalf("SourceColumns=%v", program.SourceColumns)
	}
}

// 不写 sources 时与现状一致：属性访问照旧拒绝，Program 没有 SourceColumns，MainColumns 就是 Columns。
func TestSourcesAbsentUnchanged(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := Compile(strategy, []string{"close"})
	if err != nil {
		t.Fatal(err)
	}
	if program.SourceColumns != nil || !reflect.DeepEqual(program.MainColumns(), program.Columns) || strategy.HasSources() {
		t.Fatalf("不写 sources 时出现了来源：%+v", program.SourceColumns)
	}
	if _, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: rank, score: daily.close, select: {top: 1}, weight: 1}\n")); err == nil || !strings.Contains(err.Error(), "属性访问") {
		t.Fatalf("没声明来源时 daily.close 应拒绝：%v", err)
	}
}

// sources 的保存校验：个数、来源名、bar、未声明的来源、来源之后的写法、group.by 的来源列写法、池与池成员、与主帧列同名。
func TestSourcesRejected(t *testing.T) {
	t.Parallel()
	head := "name: x\nbar: 1h\n"
	rule := "rules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	runOptionCases(t, map[string]optionCase{
		"超过两个":                {head + "sources: {a: {bar: 1d}, b: {bar: 4h}, c: {bar: 2h}}\n" + rule, "sources 至多 2 个"},
		"来源名大写":               {head + "sources: {Daily: {bar: 1d}}\n" + rule, "snake_case"},
		"来源名保留":               {head + "sources: {bars: {bar: 1d}}\n" + rule, "保留标识符"},
		"来源名是 rank":           {head + "sources: {rank: {bar: 1d}}\n" + rule, "保留标识符"},
		"缺 bar":               {head + "sources: {daily: {}}\n" + rule, "缺少 bar"},
		"bar 非法":              {head + "sources: {daily: {bar: 1w}}\n" + rule, "bar \"1w\" 不受支持"},
		"bar 不长于主帧":           {head + "sources: {daily: {bar: 1h}}\n" + rule, "必须长于策略的 bar"},
		"未知字段":                {head + "sources: {daily: {bar: 1d, every: 2}}\n" + rule, "未知字段"},
		"sources 不是对象":        {head + "sources: [daily]\n" + rule, "sources 必须是一个对象"},
		"未声明的来源":              {sourcesDSL(`score: "weekly.close"`), "weekly 不是已声明的来源"},
		"来源之后不是列名":            {sourcesDSL(`score: "daily[close]"`), "必须用 .列名 引用列"},
		"来源列再取属性":             {sourcesDSL(`score: "daily.close.x"`), "属性访问"},
		"来源访问保留列":             {sourcesDSL(`score: "daily.__params"`), "不能访问"},
		"bars 读来源":            {sourcesDSL(`score: "bars[-1].daily.close"`), "属性访问"},
		"group.by 来源未声明":      {groupBySourceDSL("daily.close", `weekly.sector`), "weekly 不是已在 sources 声明的来源"},
		"group.by 没写 sources": {"name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1, group: {by: daily.sector, max: 1}}, weight: 1}\n", "daily 不是已在 sources 声明的来源"},
		"group.by 来源列名非法":     {groupBySourceDSL("daily.close", `daily.__params`), "必须是 View 里的列名"},
		"group.by 来源列带表达式":    {groupBySourceDSL("daily.close", `"daily.sector + 1"`), "必须是 View 里的列名"},
		"group.by 来源列再取属性":    {groupBySourceDSL("daily.close", `daily.sector.x`), "必须是 View 里的列名"},
		"group.by 来源名非法":      {groupBySourceDSL("daily.close", `Daily.sector`), "必须是 View 里的列名"},
		"group.by 来源列是保留名":    {groupBySourceDSL("daily.close", `daily.score`), "必须是 View 里的列名"},
		"factor_params 读来源":   {"name: x\nbar: 1h\nsources:\n  daily: {bar: 1d}\nfactor_params:\n  ma__var: {window: {default: daily.close}}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", "属性访问"},
		"来源没被引用":              {head + "sources: {daily: {bar: 1d}}\n" + rule, "来源 daily 没有被任何表达式引用"},
		"池写 sources":          {"name: p\nbar: 1h\nsources: {daily: {bar: 1d}}\nstrategy_pool:\n  members: [{id: a, strategy: s1}]\n  allocation: {method: fixed, ratios: {a: 1}}\n", "策略池不写 sources"},
	})
	strategy, err := Parse([]byte(sourcesDSL(`score: "daily.close"`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(strategy, []string{"daily", "daily.close"}); err == nil || !strings.Contains(err.Error(), "与绑定 View 的列同名") {
		t.Fatalf("来源名与主帧列同名应拒绝：%v", err)
	}
	if _, err := Compile(strategy, []string{"close"}); err == nil || !strings.Contains(err.Error(), "daily.close") {
		t.Fatalf("来源列不存在时应报列名：%v", err)
	}
	pool, err := Parse([]byte("name: p\nbar: 1h\nstrategy_pool:\n  members: [{id: a, strategy: s1}]\n  allocation: {method: fixed, ratios: {a: 1}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	member, err := Parse([]byte("name: x\nbar: 1h\nmarket: {select: spot}\nsources:\n  daily: {bar: 1d}\nrules:\n  - {id: a, type: rank, score: daily.close, select: {top: 1}, weight: 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolMember(pool, "a", member); err == nil || !strings.Contains(err.Error(), "不能使用附加列来源") {
		t.Fatalf("池成员写 sources 应拒绝：%v", err)
	}
}

// S3 参考市场：单市场策略可以写另一个市场作参考，要求有规则写 age_markets: all；写法与取值非法时保存即拒绝。
func TestReferenceMarkets(t *testing.T) {
	t.Parallel()
	ok := "name: x\nmarket: {select: swap, prefer: swap, reference: [spot]}\nuniverse: {min_age_bars: 10, age_markets: all}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, side: short}\n"
	strategy, err := Parse([]byte(ok))
	if err != nil {
		t.Fatalf("Parse：%v", err)
	}
	if !reflect.DeepEqual(strategy.ReferenceMarkets, []string{MarketSpot}) || strategy.Market != SingleMarket(MarketSwap) {
		t.Fatalf("reference=%v market=%+v", strategy.ReferenceMarkets, strategy.Market)
	}
	plain, err := Parse([]byte("name: x\nmarket: {select: swap}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"))
	if err != nil || plain.ReferenceMarkets != nil {
		t.Fatalf("不写 reference 时应为空：%v %v", plain.ReferenceMarkets, err)
	}
	rule := "rules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, universe: {min_age_bars: 5, age_markets: all}}\n"
	runOptionCases(t, map[string]optionCase{
		"取值 equity":  {"name: x\nmarket: {select: spot, reference: [equity]}\n" + rule, "只能是 spot 或 swap"},
		"已在模式里":      {"name: x\nmarket: {select: spot, reference: [spot]}\n" + rule, "已在 select/prefer 里"},
		"双市场模式":      {"name: x\nmarket: {select: both, prefer: spot, reference: [swap]}\n" + rule, "已在 select/prefer 里"},
		"重复":         {"name: x\nmarket: {select: spot, reference: [swap, swap]}\n" + rule, "重复项"},
		"不是列表":       {"name: x\nmarket: {select: spot, reference: swap}\n" + rule, "字符串列表"},
		"没有 all 的规则": {"name: x\nmarket: {select: spot, reference: [swap]}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", "没有规则使用 age_markets: all"},
	})
	pool, err := Parse([]byte("name: p\nbar: 1h\nstrategy_pool:\n  members: [{id: a, strategy: s1}]\n  allocation: {method: fixed, ratios: {a: 1}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	member, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePoolMember(pool, "a", member); err == nil || !strings.Contains(err.Error(), "参考市场") {
		t.Fatalf("池成员写 reference 应拒绝：%v", err)
	}
}
