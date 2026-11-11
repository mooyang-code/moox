package input

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

// sourceDSL 是 1h 主帧加 1d 来源 daily 的策略：filter 读来源的两列，score 读主帧的列。
const sourceDSL = `name: with_daily
bar: 1h
sources:
  daily: {bar: 1d}
rules:
  - id: r
    type: rank
    filter: "daily.close > daily.ma__20"
    score: "ma_20"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`

// withDailySource 在 fake 上加一个日线因子 View（任务 ftask_d1d）：K 线列 close 与因子列 ma__20（成员 dma）。calendar 非空时
// 主帧与来源的数据集都声明这个日历。
func withDailySource(client *fakeClient, marketType, calendar string) {
	client.views["view_factor_d1d"] = ViewInfo{ViewID: "view_factor_d1d", DatasetID: "ds_factor_d", Frequency: "1d", Status: "active", ActiveIndexID: "idx_d",
		Columns: []ViewColumn{plainColumn("close"), factorColumn("ma__20", "dma")}}
	client.datasets["ds_factor_d"] = DatasetInfo{DatasetID: "ds_factor_d", Status: "active", Frequency: "1d", Retention: "forever", Attributes: map[string]string{"source_dataset_id": "ds_kline_d"}}
	client.datasets["ds_kline_d"] = DatasetInfo{DatasetID: "ds_kline_d", Status: "active", Frequency: "1d", Retention: "forever", Attributes: map[string]string{}, SubjectTags: []string{"binance_" + marketType}}
	client.factors["dma"] = FactorInfo{FactorID: "dma", DefinitionHash: "sha256:dma"}
	if calendar != "" {
		for id, sourceID := range map[string]string{"ds_factor": "ds_kline", "ds_factor_d": "ds_kline_d"} {
			dataset := client.datasets[id]
			dataset.Attributes = map[string]string{"source_dataset_id": sourceID, "calendar": calendar}
			client.datasets[id] = dataset
		}
	}
}

func sourceTasks() map[string]string {
	return map[string]string{"spot": "ftask_1h", "daily:spot": "ftask_d1d"}
}

// S7：来源按（来源, 市场）绑定，列以 来源名.列名 进入编译；来源的成员契约单独记，不混进主帧的 Factors；resolved_json 往返后能重新编译。
func TestResolveTasksBindsSource(t *testing.T) {
	client := newFakeClient("spot")
	withDailySource(client, "spot", "")
	resolved, program, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, sourceDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	source, ok := resolved.Sources["daily"]
	if !ok || source.Bar != "1d" || source.Calendar != DefaultCalendar || source.Tasks["spot"].ViewID != "view_factor_d1d" {
		t.Fatalf("来源绑定不符：%+v", resolved.Sources)
	}
	if source.Factors["dma"] != "sha256:dma|fixed" || len(source.Factors) != 1 || source.Columns["ma__20"].Member != "dma" || source.Columns["close"].Source != SourceDataset {
		t.Fatalf("来源的列与契约不符：%+v", source)
	}
	if _, mixed := resolved.Factors["dma"]; mixed {
		t.Fatalf("来源的成员不能混进主帧的 Factors：%v", resolved.Factors)
	}
	if got := strings.Join(program.SourceColumns["daily"], ","); got != "close,ma__20" {
		t.Fatalf("来源列：%s", got)
	}
	if got := resolved.AllTasks(); !sameStringMap(got, sourceTasks()) || !resolved.Gathers() {
		t.Fatalf("全部任务：%v", got)
	}
	if err := CheckResolvedMarkets(resolved, sourceTasks()); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseResolved(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, recompiled, err := Compile(parsed, sourceDSL); err != nil || len(recompiled.SourceColumns["daily"]) != 2 {
		t.Fatalf("按固化列重新编译失败：%v", err)
	}
	// 不写来源的策略照旧：resolved_json 里没有 sources、references 字段。
	plain, _, err := ResolveTasks(context.Background(), client, "space", map[string]string{"spot": "ftask_1h"}, parseStrategy(t, exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	plainJSON, _ := json.Marshal(plain)
	if strings.Contains(string(plainJSON), `"sources"`) || strings.Contains(string(plainJSON), `"references"`) || plain.Gathers() {
		t.Fatalf("不写来源时快照不应多出字段：%s", plainJSON)
	}
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// S7 的绑定校验：来源名未声明、声明的来源没绑任务、来源周期不是主帧的整数倍、来源 View 有可变成员、缺来源列、来源任务的市场不对。
func TestResolveTasksRejectsBadSource(t *testing.T) {
	cases := map[string]struct {
		prepare func(*fakeClient)
		dsl     string
		tasks   map[string]string
		want    string
	}{
		"未声明的来源":    {func(*fakeClient) {}, sourceDSL, map[string]string{"spot": "ftask_1h", "daily:spot": "ftask_d1d", "weekly:spot": "ftask_d1d"}, "没有声明 weekly"},
		"声明的来源没绑任务": {func(*fakeClient) {}, sourceDSL, map[string]string{"spot": "ftask_1h"}, "需要为它绑定因子任务"},
		"周期不是整数倍": {func(c *fakeClient) {
			v := c.views["view_factor_d1d"]
			v.Frequency = "90m"
			c.views["view_factor_d1d"] = v
		}, strings.Replace(sourceDSL, "{bar: 1d}", "{bar: 90m}", 1), sourceTasks(), "必须是主帧周期 1h 的整数倍"},
		"来源周期与 DSL 不符": {func(*fakeClient) {}, strings.Replace(sourceDSL, "{bar: 1d}", "{bar: 4h}", 1), sourceTasks(), "bar 断言为 4h"},
		"可变成员": {func(c *fakeClient) {
			v := c.views["view_factor_d1d"]
			v.Columns = append(append([]ViewColumn(nil), v.Columns...), memberColumn("w__var", "w__var", "w", "var"))
			c.views["view_factor_d1d"] = v
		}, sourceDSL, sourceTasks(), "来源任务不能有可变成员"},
		"缺来源列": {func(c *fakeClient) {
			v := c.views["view_factor_d1d"]
			v.Columns = []ViewColumn{plainColumn("close")}
			c.views["view_factor_d1d"] = v
		}, sourceDSL, sourceTasks(), "列 ma__20 不存在于来源 daily"},
		"来源市场不对": {func(c *fakeClient) {
			c.datasets["ds_kline_d"] = DatasetInfo{DatasetID: "ds_kline_d", Status: "active", Frequency: "1d", Attributes: map[string]string{}, SubjectTags: []string{"binance_swap"}}
		}, sourceDSL, sourceTasks(), "源数据集是 swap 市场"},
	}
	for name, tc := range cases {
		client := newFakeClient("spot")
		withDailySource(client, "spot", "")
		tc.prepare(client)
		if _, _, err := ResolveTasks(context.Background(), client, "space", tc.tasks, parseStrategy(t, tc.dsl)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：期望错误含 %q，实际 %v", name, tc.want, err)
		}
	}
	// A 股来源只能是日线、主帧必须是日内周期。
	if err := checkSourceBar("w", "cn_stock", "1h", "1d"); err != nil {
		t.Fatalf("A 股 1h 主帧加日线来源应通过：%v", err)
	}
	if err := checkSourceBar("w", "cn_stock", "30m", "1h"); err == nil || !strings.Contains(err.Error(), "只能是 1d") {
		t.Fatalf("A 股来源不是日线应拒绝：%v", err)
	}
}

// S7 as-of（加密 1h 主帧 + 1d 来源）：每根主帧读收盘时刻不晚于它的最新一根日线；00:00 那根与日线收盘对齐，读到的就是刚收盘的那根；
// 来源读到的不是应读的那一根（缺一期）时这些列按缺数处理。
func TestLoadBarReadsSourceAsOf(t *testing.T) {
	client := newFakeClient("spot")
	withDailySource(client, "spot", "")
	resolved, program, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, sourceDSL))
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC) // 2026-10-01 00:00 收盘的日线
	var sourceStarts []time.Time
	client.rows = func(query Query) ([]Row, uint64, error) {
		if query.ViewID == "view_factor_d1d" {
			sourceStarts = append(sourceStarts, query.Start)
			if !query.End.Equal(query.Start.Add(time.Nanosecond)) {
				t.Fatalf("来源只读应读的那一根：%s ~ %s", query.Start, query.End)
			}
			// ETH 只有更早一天的行：不是应读的那一根，按缺数处理。
			return append(rowsAt(day, map[string]map[string]float64{"BTC-USDT": {"close": 10, "ma__20": 9}, "SOL-USDT": {"close": 10, "ma__20": 11}}),
				rowsAt(day.Add(-24*time.Hour), map[string]map[string]float64{"ETH-USDT": {"close": 10, "ma__20": 1}})...), 7, nil
		}
		return rowsAt(query.End.Add(-time.Nanosecond), map[string]map[string]float64{"BTC-USDT": {"ma_20": 1}, "ETH-USDT": {"ma_20": 3}, "SOL-USDT": {"ma_20": 2}}), 1, nil
	}
	for name, start := range map[string]time.Time{"对齐的 00:00": day.Add(23 * time.Hour), "日内的 10:00": day.Add(34 * time.Hour)} {
		sourceStarts = nil
		loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: start, Readiness: map[string]readiness.Result{}})
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		if len(sourceStarts) != 1 || !sourceStarts[0].Equal(day) {
			t.Fatalf("%s：应读 %s 的日线，实际 %v", name, day, sourceStarts)
		}
		rows := loaded.Frame.Markets["spot"].Rows
		if rows["BTC-USDT"].Values["daily.close"] != 10 || rows["BTC-USDT"].Values["daily.ma__20"] != 9 {
			t.Fatalf("%s：来源列没有并进主帧的行：%+v", name, rows["BTC-USDT"].Values)
		}
		if _, has := rows["ETH-USDT"].Values["daily.close"]; has {
			t.Fatalf("%s：来源缺一期时不应读到旧行：%+v", name, rows["ETH-USDT"].Values)
		}
		if loaded.SourceRevisions["view_factor_d1d"] != 7 {
			t.Fatalf("%s：应记下来源的修订号：%v", name, loaded.SourceRevisions)
		}
		// BTC 过 filter（10 > 9），SOL 不过，ETH 缺数：选 BTC。
		decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Status != engine.StatusOK || len(decision.Targets) != 1 || decision.Targets[0].InstrumentID != "BTC-USDT" {
			t.Fatalf("%s：目标不符：%+v %v", name, decision.Targets, decision.Summary.Notes)
		}
	}
	// 汇合 bar 上来源的因子被整体跳过：本期不读来源，来源列按缺数处理（全部缺数，max_missing=1 时没有可选的标的）。
	sourceStarts = nil
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: day.Add(23 * time.Hour), SourceDown: map[string]string{"daily": "因子被跳过"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceStarts) != 0 {
		t.Fatalf("来源不可用时不读：%v", sourceStarts)
	}
	if _, has := loaded.Frame.Markets["spot"].Rows["BTC-USDT"].Values["daily.close"]; has {
		t.Fatal("来源不可用时来源列应缺数")
	}
}

// S7 A 股：1h 主帧 15:00 那根读当天的日线（与日线收盘对齐），10:30 那根读上一交易日的日线；日线时间键是交易日期的 UTC 零点。
func TestLoadBarStockSourceAsOf(t *testing.T) {
	client := newFakeClient("spot")
	withDailySource(client, "spot", "cn_stock")
	for _, id := range []string{"view_factor_1h", "view_factor_d1d"} {
		view := client.views[id]
		view.IndexedFrom, view.IndexedTo = time.Time{}, time.Time{}
		client.views[id] = view
	}
	resolved, program, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, sourceDSL))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Calendar != "cn_stock" || resolved.Sources["daily"].Calendar != "cn_stock" {
		t.Fatalf("日历：%s / %+v", resolved.Calendar, resolved.Sources["daily"])
	}
	var sourceStart time.Time
	client.rows = func(query Query) ([]Row, uint64, error) {
		if query.ViewID == "view_factor_d1d" {
			sourceStart = query.Start
			return rowsAt(query.Start, map[string]map[string]float64{"BTC-USDT": {"close": 2, "ma__20": 1}}), 3, nil
		}
		return rowsAt(query.End.Add(-time.Nanosecond), map[string]map[string]float64{"BTC-USDT": {"ma_20": 1}}), 1, nil
	}
	for name, tc := range map[string]struct {
		barStart, day time.Time
		aligned       bool
	}{
		"15:00 读当天":    {shAt(t, 10, 9, 14, 0), stockDay(t, 2026, 10, 9), true},
		"10:30 读上一交易日": {shAt(t, 10, 9, 9, 30), stockDay(t, 2026, 10, 8), false},
	} {
		boundary := mustPeriod(t, func() (PeriodBoundaries, error) { return FromStorageStart("cn_stock", "1h", tc.barStart) })
		if _, aligned, err := resolved.Sources["daily"].Period(boundary.BarEnd); err != nil || aligned != tc.aligned {
			t.Fatalf("%s：对齐=%v err=%v", name, aligned, err)
		}
		loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: tc.barStart})
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		sameTime(t, name, sourceStart, tc.day)
		if loaded.Frame.Markets["spot"].Rows["BTC-USDT"].Values["daily.close"] != 2 {
			t.Fatalf("%s：来源列没有并进来：%+v", name, loaded.Frame.Markets["spot"].Rows)
		}
	}
}

// 汇合口径：主帧收盘时刻等于来源收盘时刻时要等来源的 View，其余时刻只等主帧。
func TestExpectedViewsBySourceAlignment(t *testing.T) {
	client := newFakeClient("spot")
	withDailySource(client, "spot", "")
	resolved, _, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, sourceDSL))
	if err != nil {
		t.Fatal(err)
	}
	aligned, err := resolved.ExpectedViews(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || strings.Join(aligned, ",") != "view_factor_1h,view_factor_d1d" {
		t.Fatalf("对齐的那根要等两边：%v %v", aligned, err)
	}
	other, err := resolved.ExpectedViews(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	if err != nil || strings.Join(other, ",") != "view_factor_1h" {
		t.Fatalf("其余时刻只等主帧：%v %v", other, err)
	}
	if key, source, reference, ok := resolved.ViewRole("view_factor_d1d"); !ok || source != "daily" || key != "spot" || reference {
		t.Fatalf("来源 View 的角色：%s %s %v %v", key, source, reference, ok)
	}
}

// referenceDSL 是永续单市场加现货参考市场的策略：age_markets: all 要求两个市场都满 3 根。
const referenceDSL = `name: with_reference
market: {select: swap, prefer: swap, reference: [spot]}
universe: {min_age_bars: 3, age_markets: all}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 2}
    weight: 1
portfolio: {max_missing: 1}
`

// S3 参考市场：单市场加参考市场时 age_markets: all 读参考市场的历史判年龄；参考市场不进帧、不参与选择与下单。
func TestReferenceMarketAgeJudgement(t *testing.T) {
	client := newDualClient()
	for _, id := range []string{"view_factor_1h", "view_factor_swap1h"} {
		view := client.views[id]
		view.SeriesBars = 1000
		client.views[id] = view
	}
	tasks := map[string]string{"swap": "ftask_swap1h", "spot": "ftask_1h"}
	resolved, program, err := ResolveTasks(context.Background(), client, "space", tasks, parseStrategy(t, referenceDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resolved.Market.Select != "swap" || len(resolved.Tasks) != 1 || resolved.References["spot"].ViewID != "view_factor_1h" || resolved.Gathers() {
		t.Fatalf("参考市场不进主帧：%+v", resolved)
	}
	if err := CheckResolvedMarkets(resolved, tasks); err != nil {
		t.Fatal(err)
	}
	if _, _, reference, ok := resolved.ViewRole("view_factor_1h"); !ok || !reference {
		t.Fatal("现货 View 是参考市场")
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		if len(query.Columns) == 1 && query.Columns[0] == "close" && query.End.Before(barStart) {
			// 年龄探针：永续上 BTC、ETH 都满 3 根；现货上只有 BTC。
			if query.ViewID == "view_factor_swap1h" {
				return rowsAt(query.Start, map[string]map[string]float64{"BTC-USDT": {"close": 1}, "ETH-USDT": {"close": 1}}), 1, nil
			}
			return rowsAt(query.Start, map[string]map[string]float64{"BTC-USDT": {"close": 1}}), 1, nil
		}
		if query.ViewID == "view_factor_1h" {
			t.Fatalf("参考市场不读行：%+v", query)
		}
		return rowsAt(barStart, map[string]map[string]float64{"BTC-USDT": {"ma_20": 1}, "ETH-USDT": {"ma_20": 2}, "DOGE-USDT": {"ma_20": 3}}), 1, nil
	}
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, Readiness: map[string]readiness.Result{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, inFrame := loaded.Frame.Markets["spot"]; inFrame || len(loaded.Frame.Markets) != 1 {
		t.Fatalf("参考市场不进帧：%+v", loaded.Frame.Markets)
	}
	aged := strings.Join(loaded.Sets.AgedOut["r"], ",")
	if aged != "DOGE-USDT,ETH-USDT" {
		t.Fatalf("ETH 在现货不满 3 根、DOGE 现货没有：都应剔除，实际 %s", aged)
	}
	if reason := loaded.Sets.AgedOutReasons["r"]["DOGE-USDT"]; reason != engine.ReasonAgeMarkets {
		t.Fatalf("DOGE 现货没有上线，原因应是 age_markets：%q", reason)
	}
	decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != engine.StatusOK || len(decision.Targets) != 1 || decision.Targets[0].InstrumentID != "BTC-USDT" || decision.Targets[0].Market != "swap" {
		t.Fatalf("目标不符：%+v %v", decision.Targets, decision.Summary.Notes)
	}
	// 参考市场的绑定校验：DSL 写了参考市场却没登记任务；单市场写了 all 但没有参考市场照旧拒绝。
	if _, _, err := ResolveTasks(context.Background(), client, "space", map[string]string{"swap": "ftask_swap1h"}, parseStrategy(t, referenceDSL)); err == nil || !strings.Contains(err.Error(), "需要绑定这个市场的因子任务") {
		t.Fatalf("没登记参考任务应拒绝：%v", err)
	}
	noReference := strings.Replace(referenceDSL, ", reference: [spot]", "", 1)
	if _, _, err := ResolveTasks(context.Background(), client, "space", map[string]string{"swap": "ftask_swap1h"}, parseStrategy(t, noReference)); err == nil || !strings.Contains(err.Error(), "market.reference") {
		t.Fatalf("单市场写 all 又没有参考市场应拒绝：%v", err)
	}
	if _, _, err := ResolveTasks(context.Background(), client, "space", map[string]string{"swap": "ftask_swap1h", "spot": "ftask_swap1h"}, parseStrategy(t, referenceDSL)); err == nil || !strings.Contains(err.Error(), "源数据集是 swap 市场") {
		t.Fatalf("参考任务的市场不对应拒绝：%v", err)
	}
}

// groupSourceDSL 是 1h 主帧加 1d 来源 daily 的策略：只有 select.group.by 引用来源（按日线的行业列分组，每组最多 1 个）。
const groupSourceDSL = `name: group_by_daily
bar: 1h
sources:
  daily: {bar: 1d}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 2, group: {by: daily.industry, max: 1}}
    weight: 1
portfolio: {max_missing: 1}
`

// select.group.by 写来源列：来源列进必需列、按 as-of 并进主帧的行后参与分组名额；来源缺一期的标的按缺数处理（missing:daily.industry），
// 与主帧的分组列口径一致。来源 View 没有这一列时启用解析报错。
func TestLoadBarGroupBySourceColumn(t *testing.T) {
	client := newFakeClient("spot")
	withDailySource(client, "spot", "")
	if _, _, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, groupSourceDSL)); err == nil || !strings.Contains(err.Error(), "industry") {
		t.Fatalf("来源 View 没有 group.by 的列时应报错：%v", err)
	}
	view := client.views["view_factor_d1d"]
	view.Columns = append(view.Columns, plainColumn("industry"))
	client.views["view_factor_d1d"] = view
	resolved, program, err := ResolveTasks(context.Background(), client, "space", sourceTasks(), parseStrategy(t, groupSourceDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := program.SourceColumns["daily"]; len(got) != 1 || got[0] != "industry" {
		t.Fatalf("来源的必需列应是 industry：%v", program.SourceColumns)
	}
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	evaluate := func(daily map[string]map[string]float64) engine.Decision {
		t.Helper()
		client.rows = func(query Query) ([]Row, uint64, error) {
			if query.ViewID == "view_factor_d1d" {
				if len(query.Columns) != 1 || query.Columns[0] != "industry" {
					t.Fatalf("来源应只读 industry：%v", query.Columns)
				}
				return rowsAt(day, daily), 7, nil
			}
			return rowsAt(query.End.Add(-time.Nanosecond), map[string]map[string]float64{"BTC-USDT": {"ma_20": 3}, "ETH-USDT": {"ma_20": 2}, "SOL-USDT": {"ma_20": 1}}), 1, nil
		}
		loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: day.Add(34 * time.Hour), Readiness: map[string]readiness.Result{}})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
		if err != nil {
			t.Fatal(err)
		}
		if decision.Status != engine.StatusOK {
			t.Fatalf("状态 %s：%v", decision.Status, decision.Summary.Notes)
		}
		return decision
	}
	reasonOf := func(decision engine.Decision, id string) string {
		for _, item := range decision.Items {
			if item.InstrumentID == id {
				return item.Reason
			}
		}
		return ""
	}
	targets := func(decision engine.Decision) string {
		ids := make([]string, 0, len(decision.Targets))
		for _, target := range decision.Targets {
			ids = append(ids, target.InstrumentID)
		}
		return strings.Join(sortedStrings(ids), ",")
	}
	// BTC、ETH 同属行业 1，SOL 属行业 2：按名次 BTC 占行业 1 的名额，ETH 组满跳过，SOL 入选。
	decision := evaluate(map[string]map[string]float64{"BTC-USDT": {"industry": 1}, "ETH-USDT": {"industry": 1}, "SOL-USDT": {"industry": 2}})
	if targets(decision) != "BTC-USDT,SOL-USDT" || reasonOf(decision, "ETH-USDT") != "group_full" {
		t.Fatalf("分组名额不符：%s，ETH=%s", targets(decision), reasonOf(decision, "ETH-USDT"))
	}
	// BTC 的日线缺了这一期：按缺数处理，不参与排名与分组；ETH 接上行业 1 的名额。
	decision = evaluate(map[string]map[string]float64{"ETH-USDT": {"industry": 1}, "SOL-USDT": {"industry": 2}})
	if targets(decision) != "ETH-USDT,SOL-USDT" || reasonOf(decision, "BTC-USDT") != "missing:daily.industry" {
		t.Fatalf("来源缺数的处理不符：%s，BTC=%s", targets(decision), reasonOf(decision, "BTC-USDT"))
	}
}
