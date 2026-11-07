package input

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	spotTag = "venue:binance|market:spot|source:spot_http"
	swapTag = "venue:binance|market:swap|source:swap_http"
)

// unifiedDSL 是统一数据集上的策略：必须写 market。
const unifiedDSL = `name: unified
market: {select: both, prefer: swap}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`

// newUnifiedClient 构造统一数据集上的因子任务（ftask_1h）：一个结果 View，源数据集 market_type=multi，
// 挂现货、永续两个标签；现货标签有 BTC、ETH、SOL，永续标签有 BTC、ETH、DOGE。
func newUnifiedClient() *fakeClient {
	client := newFakeClient("spot")
	client.datasets["ds_kline"] = DatasetInfo{DatasetID: "ds_kline", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{"market_type": "multi"}, SubjectTags: []string{"binance_spot", "binance_swap"}}
	factor := client.datasets["ds_factor"]
	factor.SubjectTags = []string{"binance_spot", "binance_swap"}
	client.datasets["ds_factor"] = factor
	client.subjects["ds_factor"] = []Subject{
		{SubjectID: "BTC-USDT", SeriesTag: spotTag, Active: true}, {SubjectID: "ETH-USDT", SeriesTag: spotTag, Active: true},
		{SubjectID: "SOL-USDT", SeriesTag: spotTag, Active: true}, {SubjectID: "DOGE-USDT", SeriesTag: swapTag, Active: true},
		{SubjectID: "OLD-USDT", SeriesTag: spotTag, Active: false},
	}
	client.tags["binance_spot"] = []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
	client.tags["binance_swap"] = []string{"BTC-USDT", "ETH-USDT", "DOGE-USDT"}
	return client
}

func TestResolveUnifiedDatasetRequiresMarket(t *testing.T) {
	client := newUnifiedClient()
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, unifiedDSL))
	if err != nil {
		t.Fatalf("统一数据集上的任务应能解析：%v", err)
	}
	if !resolved.Unified() || len(resolved.Tasks) != 1 || resolved.Tasks[MarketMulti].ViewID != "view_factor_1h" || program == nil {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	if got := strings.Join(resolved.Markets(), ","); got != "spot,swap" {
		t.Fatalf("市场按 DSL 的模式取：%s", got)
	}
	if resolved.TaskOf("swap").ViewID != "view_factor_1h" || resolved.TaskKey("spot") != MarketMulti || strings.Join(resolved.ViewIDs(), ",") != "view_factor_1h" {
		t.Fatalf("两个市场共用一个 View：%+v", resolved)
	}
	if err := CheckResolvedMarkets(resolved, map[string]string{MarketMulti: "ftask_1h"}); err != nil {
		t.Fatal(err)
	}
	// 统一数据集上的任务写成现货（试算、回放按市场给 factor_tasks 时的常见错误）：提示写 multi 或从实例发起。
	if err := CheckResolvedMarkets(resolved, map[string]string{"spot": "ftask_1h"}); err == nil || !strings.Contains(err.Error(), "multi=ftask_1h") || !strings.Contains(err.Error(), "从实例发起") {
		t.Fatalf("应提示写 multi 或从实例发起：%v", err)
	}
	raw, _ := json.Marshal(resolved)
	parsed, err := ParseResolved(raw)
	if err != nil || !parsed.Unified() {
		t.Fatalf("resolved_json 往返失败：%+v err=%v", parsed, err)
	}

	// 五种模式在统一数据集上都能解析；纯现货、纯永续只涉及一个市场。
	for _, mode := range []string{"{select: spot, prefer: spot}", "{select: swap, prefer: swap}", "{select: both, prefer: spot}", "{select: both, prefer: swap}", "{select: spot, prefer: swap}"} {
		dslText := strings.Replace(unifiedDSL, "{select: both, prefer: swap}", mode, 1)
		if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, dslText)); err != nil {
			t.Errorf("模式 %s 应能在统一数据集上解析：%v", mode, err)
		}
	}
}

func TestResolveUnifiedDatasetRejections(t *testing.T) {
	client := newUnifiedClient()
	noMarket := strings.Replace(unifiedDSL, "market: {select: both, prefer: swap}\n", "", 1)
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, noMarket)); err == nil || !strings.Contains(err.Error(), "必须写 market") {
		t.Fatalf("统一数据集上不写 market 应被拒绝：%v", err)
	}
	tuned := strings.Replace(strings.Replace(unifiedDSL, "{select: both, prefer: swap}", "{select: spot, prefer: spot}", 1), "rules:", "factor_params:\n  bias__var:\n    window: 20\nrules:", 1)
	if _, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, tuned)); err == nil || !strings.Contains(err.Error(), "factor_params") {
		t.Fatalf("统一数据集上的 factor_params 应被拒绝：%v", err)
	}
	// 统一数据集的任务已覆盖两个市场，不能再和别的任务一起绑定。
	dual := newDualClient()
	dual.datasets["ds_kline"] = client.datasets["ds_kline"]
	if _, _, err := Resolve(context.Background(), dual, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, unifiedDSL)); err == nil || !strings.Contains(err.Error(), "统一数据集") {
		t.Fatalf("统一数据集的任务不能与其他任务并存：%v", err)
	}
}

func TestCheckRegisteredMarketsUnified(t *testing.T) {
	marketOf := func(context.Context, string) (string, error) { return MarketMulti, nil }
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{MarketMulti: "ftask_1h"}, marketOf); err != nil {
		t.Fatalf("统一数据集登记为 multi 应通过：%v", err)
	}
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{"spot": "ftask_1h"}, marketOf); err == nil || !strings.Contains(err.Error(), "登记为 multi") {
		t.Fatalf("统一数据集登记成现货应被拒绝并提示 multi：%v", err)
	}
	spotOf := func(context.Context, string) (string, error) { return "spot", nil }
	if err := CheckRegisteredMarkets(context.Background(), map[string]string{MarketMulti: "ftask_1h"}, spotOf); err == nil {
		t.Fatal("单市场数据集登记成 multi 应被拒绝")
	}
}

// unifiedRows 是统一数据集 View 里一个周期的行：每个标的在现货、永续两条序列上各一行（只给出有的）。
func unifiedRows(at time.Time, spot, swap map[string]float64) []Row {
	var rows []Row
	for id, value := range spot {
		rows = append(rows, Row{SubjectID: id, SeriesTag: spotTag, DataTime: at, Values: map[string]float64{"ma_20": value, "close": 1}})
	}
	for id, value := range swap {
		rows = append(rows, Row{SubjectID: id, SeriesTag: swapTag, DataTime: at, Values: map[string]float64{"ma_20": value, "close": 1}})
	}
	return rows
}

// 统一数据集：只读一次（selector 不带 series_tag），按 series_tag 对应的市场拆行；每个市场的标的取自己标签的活跃成员；
// 同一标的现货、永续两条序列并存不是歧义。
func TestLoadBarUnifiedReadsOnceAndSplitsByMarket(t *testing.T) {
	client := newUnifiedClient()
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, unifiedDSL))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		// OLD-USDT 不是任何标签的活跃成员；DOGE 的现货行不属于现货标签成员。
		return unifiedRows(barStart, map[string]float64{"BTC-USDT": 1, "ETH-USDT": 2, "DOGE-USDT": 50}, map[string]float64{"BTC-USDT": 10, "DOGE-USDT": 4}), 7, nil
	}
	universe := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "DOGE-USDT", "OLD-USDT"}
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: map[string][]string{"spot": universe, "swap": universe}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.queries) != 1 {
		t.Fatalf("统一数据集一个 View 只读一次：%d 次", len(client.queries))
	}
	for _, subject := range client.queries[0].Subjects {
		if subject.SeriesTag != "" {
			t.Fatalf("统一数据集读全部序列，selector 不能带 series_tag：%+v", subject)
		}
	}
	spot, swap := loaded.Frame.Markets["spot"], loaded.Frame.Markets["swap"]
	if len(spot.Rows) != 2 || spot.Rows["BTC-USDT"].Values["ma_20"] != 1 || spot.Rows["ETH-USDT"].Values["ma_20"] != 2 {
		t.Fatalf("现货行不符（DOGE 不是现货成员）：%+v", spot.Rows)
	}
	if len(swap.Rows) != 2 || swap.Rows["BTC-USDT"].Values["ma_20"] != 10 || swap.Rows["DOGE-USDT"].Values["ma_20"] != 4 {
		t.Fatalf("永续行不符：%+v", swap.Rows)
	}
	if got := strings.Join(loaded.Frame.Universe, ","); got != "BTC-USDT,DOGE-USDT,ETH-USDT,SOL-USDT" {
		t.Fatalf("基础集合是两个市场各自的活跃标的的并集（OLD 已停用）：%s", got)
	}
}

// 歧义只在同一个市场内判断：同一标的的现货序列出现两个 tag，或序列换不出市场，才整期跳过；单市场数据集上同标的多序列仍是歧义。
func TestLoadBarUnifiedAmbiguity(t *testing.T) {
	cases := map[string][]Row{
		"同一市场两个序列": append(unifiedRows(barStart, map[string]float64{"BTC-USDT": 1}, nil),
			Row{SubjectID: "BTC-USDT", SeriesTag: "venue:binance|market:spot|source:backup", DataTime: barStart, Values: map[string]float64{"ma_20": 2}}),
		"序列换不出市场": {{SubjectID: "BTC-USDT", SeriesTag: "venue:binance|market:option", DataTime: barStart, Values: map[string]float64{"ma_20": 2}}},
	}
	for name, rows := range cases {
		client := newUnifiedClient()
		resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, unifiedDSL))
		if err != nil {
			t.Fatal(err)
		}
		client.rows = func(Query) ([]Row, uint64, error) { return rows, 1, nil }
		_, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: map[string][]string{"spot": {"BTC-USDT"}, "swap": {"BTC-USDT"}}})
		var skip *SkipError
		if !errors.As(err, &skip) || skip.Reason != SkipAmbiguousSeries {
			t.Errorf("%s：应按 ambiguous_series 跳过：%v", name, err)
		}
	}
	// 单市场数据集：同一标的同一周期两个序列仍是歧义。
	single := newFakeClient("spot")
	resolved, program, err := Resolve(context.Background(), single, "space", []string{"ftask_1h"}, parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	single.rows = func(Query) ([]Row, uint64, error) {
		return []Row{{SubjectID: "ETH-USDT", SeriesTag: spotTag, DataTime: barStart}, {SubjectID: "ETH-USDT", SeriesTag: swapTag, DataTime: barStart}}, 1, nil
	}
	if _, err := (Loader{Client: single}).LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart}); err == nil || !strings.Contains(err.Error(), "多个序列") {
		t.Fatalf("单市场数据集上同标的多序列仍是歧义：%v", err)
	}
}

// 研究回放的分段读取：统一数据集上同一标的的现货、永续两条序列互不歧义，每个市场的读取器只留自己的序列；
// 单市场读取器（没有 Market）里同标的多序列仍是歧义。
func TestRangeLoaderFiltersByMarket(t *testing.T) {
	client := newUnifiedClient()
	view := client.views["view_factor_1h"]
	client.rows = func(Query) ([]Row, uint64, error) {
		rows := unifiedRows(barStart, map[string]float64{"BTC-USDT": 1}, map[string]float64{"BTC-USDT": 10})
		return append(rows, Row{SubjectID: "ETH-USDT", SeriesTag: "venue:binance|market:option", DataTime: barStart}), 1, nil
	}
	load := func(market string) RangeRows {
		loader := &RangeLoader{Client: client, SpaceID: "space", View: view, Subjects: []Subject{{SubjectID: "BTC-USDT"}, {SubjectID: "ETH-USDT"}}, Market: market}
		rows, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	key := barStart.Unix()
	spot, swap := load("spot"), load("swap")
	if spot.Bars[key]["BTC-USDT"].Values["ma_20"] != 1 || swap.Bars[key]["BTC-USDT"].Values["ma_20"] != 10 {
		t.Fatalf("每个市场只留自己的序列：%+v %+v", spot.Bars[key], swap.Bars[key])
	}
	if len(spot.Ambiguous[key]) != 1 || len(swap.Ambiguous[key]) != 1 || spot.Ambiguous[key]["ETH-USDT"] == "" {
		t.Fatalf("换不出市场的序列两个市场都歧义，现货、永续并存不歧义：%+v %+v", spot.Ambiguous, swap.Ambiguous)
	}
	both := load("")
	if both.Ambiguous[key]["BTC-USDT"] == "" {
		t.Fatalf("没有 Market 时同标的多序列仍是歧义：%+v", both.Ambiguous)
	}
}

// 年龄探针按（标的、市场）记录：同一个 View 里现货、永续分别记，统一数据集的两个市场每期交替探针也只在第一次读持久化。
func TestAgeProbeIsPerMarketOnUnifiedDataset(t *testing.T) {
	client := newUnifiedClient()
	strategy := parseStrategy(t, strings.Replace(unifiedDSL, "rules:", "universe: {min_age_bars: 24}\nrules:", 1))
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	probes := 0
	client.rows = func(query Query) ([]Row, uint64, error) {
		if len(query.Columns) == 1 && query.Columns[0] == "close" {
			probes++
			// 窗口里只有现货的历史：BTC、ETH 的永续刚上线。
			return unifiedRows(query.Start, map[string]float64{"BTC-USDT": 1, "ETH-USDT": 1, "SOL-USDT": 1}, map[string]float64{"DOGE-USDT": 1}), 1, nil
		}
		return unifiedRows(barStart, map[string]float64{"BTC-USDT": 1, "ETH-USDT": 2, "SOL-USDT": 3}, map[string]float64{"BTC-USDT": 10, "ETH-USDT": 11, "DOGE-USDT": 4}), 1, nil
	}
	store := &memoryAgeStore{saved: map[string][]string{}}
	meta := NewMetaCache()
	meta.Ages = store
	universe := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "DOGE-USDT"}
	load := func() Loaded {
		loaded, err := Loader{Client: client, Meta: meta}.ForEvent().LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: map[string][]string{"spot": universe, "swap": universe}})
		if err != nil {
			t.Fatal(err)
		}
		return loaded
	}
	loaded := load()
	// 永续刚上线的 BTC、ETH 年龄不够：行保留在帧里，但记在永续的 AgedOut 里，由选币池构造决定（不借现货的历史、持仓豁免）。
	if aged := loaded.Frame.Markets["swap"].AgedOut; len(aged) != 2 || !hasKey(aged, "BTC-USDT") || !hasKey(aged, "ETH-USDT") {
		t.Fatalf("永续刚上线的 BTC、ETH 不能借用现货的历史通过年龄检查：%v", aged)
	}
	if len(loaded.Frame.Markets["spot"].AgedOut) != 0 || len(loaded.Frame.Markets["spot"].Rows) != 3 || loaded.Frame.Markets["swap"].Rows["DOGE-USDT"].Values["ma_20"] != 4 {
		t.Fatalf("现货三个标的都通过，永续只有 DOGE：%+v", loaded.Frame.Markets)
	}
	if len(store.saved) != 2 {
		t.Fatalf("通过记录按市场分开保存：%v", store.saved)
	}
	before := probes
	load()
	if probes-before > 2 {
		t.Fatalf("第二期不应再为已通过的标的探针（两个市场交替也只读一次持久化）：%d", probes-before)
	}
}

// 策略池暂不支持统一数据集上的任务：成员账本估值与预热按市场分别读取 View。
func TestResolvePoolRejectsUnifiedDatasetTask(t *testing.T) {
	client := newUnifiedClient()
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	_, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, poolDSL), members)
	if err == nil || !strings.Contains(err.Error(), "策略池暂不支持") {
		t.Fatalf("统一数据集上的任务不能绑定策略池：%v", err)
	}
}

func hasKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// 启用时按市场核对 min_age_bars 的历史：View 级覆盖统计取所有序列的最早一行，现货历史够、永续刚导入很短时它会放行，
// 之后永续一直被当新上市；各市场的序列都要在目标根附近有数据才能启用（现货、永续各一个任务时本来就是分别校验的）。
func TestCheckCoverageUnifiedRequiresHistoryPerMarket(t *testing.T) {
	client := newUnifiedClient()
	strategy := parseStrategy(t, strings.Replace(unifiedDSL, "rules:", "universe: {min_age_bars: 24}\nrules:", 1))
	resolved, _, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	now := barStart.Add(time.Hour)
	client.rows = func(query Query) ([]Row, uint64, error) {
		return unifiedRows(query.Start, map[string]float64{"BTC-USDT": 1}, nil), 1, nil // 窗口里只有现货的历史
	}
	err = CheckCoverage(context.Background(), client, "space", resolved, now)
	if err == nil || !strings.Contains(err.Error(), "永续市场的序列") {
		t.Fatalf("永续历史不够应拒绝启用并点名永续：%v", err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		return unifiedRows(query.Start, map[string]float64{"BTC-USDT": 1}, map[string]float64{"DOGE-USDT": 1}), 1, nil
	}
	if err := CheckCoverage(context.Background(), client, "space", resolved, now); err != nil {
		t.Fatalf("两个市场历史都够应放行：%v", err)
	}
}

// 暂存的键含标的内容：个数相同、标的不同的两次读取不能共用一次结果。
func TestRowsMemoKeyDistinguishesSubjects(t *testing.T) {
	client := newUnifiedClient()
	view := client.views["view_factor_1h"]
	client.rows = func(query Query) ([]Row, uint64, error) {
		var rows []Row
		for _, subject := range query.Subjects {
			rows = append(rows, Row{SubjectID: subject.SubjectID, SeriesTag: spotTag, DataTime: barStart, Values: map[string]float64{"ma_20": 1}})
		}
		return rows, 1, nil
	}
	memo := &RowsMemo{}
	load := func(ids ...string) RangeRows {
		subjects := make([]Subject, 0, len(ids))
		for _, id := range ids {
			subjects = append(subjects, Subject{SubjectID: id})
		}
		rows, err := (&RangeLoader{Client: client, SpaceID: "space", View: view, Subjects: subjects, Market: "spot", Memo: memo}).Load(context.Background(), barStart, barStart.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	first, same, other := load("BTC-USDT", "ETH-USDT"), load("BTC-USDT", "ETH-USDT"), load("BTC-USDT", "SOL-USDT")
	if len(client.queries) != 2 {
		t.Fatalf("相同的读取共用一次、不同的标的各读一次：%d 次", len(client.queries))
	}
	if len(first.Bars[barStart.Unix()]) != 2 || len(same.Bars[barStart.Unix()]) != 2 {
		t.Fatalf("共用的读取结果不符：%+v", same.Bars)
	}
	if _, ok := other.Bars[barStart.Unix()]["SOL-USDT"]; !ok {
		t.Fatalf("标的不同的读取要读自己的：%+v", other.Bars)
	}
	memo.Reset()
	load("BTC-USDT", "ETH-USDT")
	if len(client.queries) != 3 {
		t.Fatalf("Reset 之后要重新读：%d 次", len(client.queries))
	}
}
