package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// compile 解析并编译一段 DSL，columns 是 View 的全部列。
func compile(t *testing.T, raw string, columns ...string) *dsl.Program {
	t.Helper()
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("解析 DSL 失败：%v", err)
	}
	program, err := dsl.Compile(strategy, columns)
	if err != nil {
		t.Fatalf("编译 DSL 失败：%v", err)
	}
	return program
}

// values 构造一行当期列值。
func values(pairs ...any) Row {
	row := Row{Values: make(map[string]float64, len(pairs)/2)}
	for i := 0; i+1 < len(pairs); i += 2 {
		row.Values[pairs[i].(string)] = toTestFloat(pairs[i+1])
	}
	return row
}

func toTestFloat(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case float64:
		return typed
	default:
		panic(fmt.Sprintf("测试数据不支持的类型 %T", value))
	}
}

// frameOf 用行集合构造一帧：U 为全部行的标的，每条规则的 E(r) 默认等于 U。
func frameOf(program *dsl.Program, rows map[string]Row) Frame {
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	frame := Frame{BarEnd: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Rows: rows, Universe: ids, Expected: map[string][]string{}, AgedOut: map[string][]string{}}
	for _, rule := range program.Rules {
		frame.Expected[rule.Rule.ID] = append([]string(nil), ids...)
	}
	return frame
}

func evaluate(t *testing.T, program *dsl.Program, frame Frame, previous State) Decision {
	t.Helper()
	decision, err := Evaluate(program, frame, previous)
	if err != nil {
		t.Fatalf("求值失败：%v", err)
	}
	assertUniqueItems(t, decision)
	return decision
}

// assertUniqueItems 校验每个 (规则, 标的) 只有一条解释明细，与 t_strategy_result_items 的主键一致。
func assertUniqueItems(t *testing.T, decision Decision) {
	t.Helper()
	seen := make(map[string]struct{}, len(decision.Items))
	for _, item := range decision.Items {
		key := item.RuleID + "/" + item.InstrumentID
		if _, ok := seen[key]; ok {
			t.Fatalf("解释明细重复：%s（全部 %+v）", key, decision.Items)
		}
		seen[key] = struct{}{}
	}
}

func weightsOf(decision Decision) map[string]string {
	out := make(map[string]string, len(decision.Targets))
	for _, target := range decision.Targets {
		out[target.InstrumentID] = target.Weight.String()
	}
	return out
}

// assertWeights 按定点数值比较目标权重。
func assertWeights(t *testing.T, decision Decision, want map[string]string) {
	t.Helper()
	got := weightsOf(decision)
	if len(got) != len(want) {
		t.Fatalf("目标数量不符：got %v want %v", got, want)
	}
	for id, raw := range want {
		actual, ok := got[id]
		if !ok {
			t.Fatalf("缺少目标 %s：got %v", id, got)
		}
		if quant.Must(actual).Cmp(quant.Must(raw)) != 0 {
			t.Fatalf("%s 的权重不符：got %s want %s（全部 %v）", id, actual, raw, got)
		}
	}
}

func assertDecimal(t *testing.T, label, got, want string) {
	t.Helper()
	if quant.Must(got).Cmp(quant.Must(want)) != 0 {
		t.Fatalf("%s 不符：got %s want %s", label, got, want)
	}
}

func assertOK(t *testing.T, decision Decision) {
	t.Helper()
	if decision.Status != StatusOK {
		t.Fatalf("期望 ok，实际 %s(%s)：%v", decision.Status, decision.SkipReason, decision.Summary.Notes)
	}
}

func assertSkipped(t *testing.T, decision Decision, reason string) {
	t.Helper()
	if decision.Status != StatusSkipped || decision.SkipReason != reason {
		t.Fatalf("期望 skipped(%s)，实际 %s(%s)", reason, decision.Status, decision.SkipReason)
	}
	if len(decision.Targets) != 0 {
		t.Fatalf("跳过时不应有目标：%v", decision.Targets)
	}
}

func findItem(t *testing.T, decision Decision, ruleID, instrumentID string) Item {
	t.Helper()
	for _, item := range decision.Items {
		if item.RuleID == ruleID && item.InstrumentID == instrumentID {
			return item
		}
	}
	t.Fatalf("没有 %s/%s 的解释明细：%v", ruleID, instrumentID, decision.Items)
	return Item{}
}

func countItems(decision Decision, ruleID, stage string) int {
	count := 0
	for _, item := range decision.Items {
		if item.RuleID == ruleID && item.Stage == stage {
			count++
		}
	}
	return count
}

// fingerprint 把决策序列化成可逐字节比较的文本。
func fingerprint(t *testing.T, decision Decision) string {
	t.Helper()
	type target struct {
		ID     string `json:"id"`
		Weight string `json:"weight"`
	}
	targets := make([]target, 0, len(decision.Targets))
	for _, item := range decision.Targets {
		targets = append(targets, target{ID: item.InstrumentID, Weight: item.Weight.String()})
	}
	raw, err := json.Marshal(struct {
		Status  string
		Reason  string
		Targets []target
		State   State
		Summary Summary
		Items   []Item
	}{decision.Status, decision.SkipReason, targets, decision.State, decision.Summary, decision.Items})
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return string(raw)
}

func numberedIDs(prefix string, n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("%s%03d-USDT", prefix, i))
	}
	return ids
}

const exampleStrategy = `name: binance_spot_momentum_1h
bar: 1h

universe:
  min_age_bars: 240
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]

rules:
  - id: long_momentum
    name: 多头动量选币
    type: rank
    filter: "quote_volume_mean_20 > 2000000 && close > 0"
    score: "0.6 * rank(bias_q_20) + 0.4 * rank(quote_volume_mean_q_20)"
    select: {top: 5, buffer: 2}
    weight: {total: 0.8, method: equal, cap: 0.3}

  - id: btc_trend
    name: BTC 均线趋势跟随
    type: signal
    pool: [BTC-USDT]
    entry: "bars[-1].close <= bars[-1].ma_20 && bars[0].close > bars[0].ma_20"
    exit:  "bars[0].close < bars[0].ma_20"
    weight: {total: 0.2}

portfolio:
  leverage: 1
  max_weight: 0.3
  min_universe: 3
  max_missing: 0.2
`

var exampleColumns = []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "bias_q_20", "quote_volume_mean_20", "quote_volume_mean_q_20", "ma_20"}

// exampleFrame 构造设计文档示例策略的一帧：8 个山寨币通过 filter，BTC 走 signal 规则。
func exampleFrame(program *dsl.Program) Frame {
	rows := map[string]Row{}
	alts := numberedIDs("ALT", 8)
	for i, id := range alts {
		rows[id] = values("close", 10+i, "quote_volume_mean_20", 3000000, "bias_q_20", float64(i)/10, "quote_volume_mean_q_20", float64(8-i)/10)
	}
	rows["LOW-USDT"] = values("close", 1, "quote_volume_mean_20", 1000, "bias_q_20", 0.9, "quote_volume_mean_q_20", 0.9)
	rows["BTC-USDT"] = Row{Values: map[string]float64{"close": 101, "ma_20": 100}, Previous: map[string]float64{"close": 99, "ma_20": 100}}
	frame := frameOf(program, rows)
	frame.Spot = true
	expected := make([]string, 0, len(alts)+1)
	expected = append(expected, alts...)
	expected = append(expected, "LOW-USDT")
	frame.Expected["long_momentum"] = expected
	frame.Expected["btc_trend"] = []string{"BTC-USDT"}
	frame.Universe = append(expected, "BTC-USDT")
	return frame
}

func TestExampleStrategyEvaluatesDeterministically(t *testing.T) {
	program := compile(t, exampleStrategy, exampleColumns...)
	frame := exampleFrame(program)
	first := evaluate(t, program, frame, State{})
	assertOK(t, first)
	// 分数随序号递增：ALT003～ALT007 入选，各 0.16；BTC 入场 0.2。
	want := map[string]string{"BTC-USDT": "0.2"}
	for _, id := range numberedIDs("ALT", 8)[3:] {
		want[id] = "0.16"
	}
	assertWeights(t, first, want)
	if item := findItem(t, first, "long_momentum", "ALT002-USDT"); item.Stage != StageScored || item.Reason != "not_selected" || item.Rank != 6 {
		t.Fatalf("ALT002 应排第 6 名未入选：%+v", item)
	}
	assertDecimal(t, "gross", first.Summary.Gross, "1")
	assertDecimal(t, "cash", first.Summary.Cash, "0")
	if first.Summary.Rules["long_momentum"].Filtered != 1 {
		t.Fatalf("LOW-USDT 应被 filter 淘汰：%+v", first.Summary.Rules["long_momentum"])
	}
	if item := findItem(t, first, "btc_trend", "BTC-USDT"); item.Stage != StageWeighted {
		t.Fatalf("BTC 应进入权重：%+v", item)
	}
	second := evaluate(t, program, frame, State{})
	if fingerprint(t, first) != fingerprint(t, second) {
		t.Fatalf("同一输入两次求值结果不同：\n%s\n%s", fingerprint(t, first), fingerprint(t, second))
	}
	// 解释明细按规则顺序、标的 id 排序。
	for i := 1; i < len(first.Items); i++ {
		a, b := first.Items[i-1], first.Items[i]
		if a.RuleID == b.RuleID && a.InstrumentID > b.InstrumentID {
			t.Fatalf("解释明细未排序：%+v 在 %+v 之前", a, b)
		}
	}
}

func TestEvaluateRejectsNilProgram(t *testing.T) {
	if _, err := Evaluate(nil, Frame{}, State{}); err == nil {
		t.Fatal("空程序应返回错误")
	}
}

// 组合约束失败（现货出现负权重）按 config_error 跳过，解释明细仍按规则声明顺序与标的 id 排序。
func TestConfigErrorSkipKeepsItemsSorted(t *testing.T) {
	program := compile(t, `name: bad
rules:
  - id: b
    type: rank
    score: "m"
    select: {bottom: 2}
    weight: {total: 0.5}
    side: short
  - id: a
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
`, "m")
	frame := frameOf(program, map[string]Row{"C": values("m", 3), "A": values("m", 1), "B": values("m", 2)})
	frame.Spot = true
	first := evaluate(t, program, frame, State{})
	assertSkipped(t, first, SkipConfigError)
	var order []string
	for _, item := range first.Items {
		order = append(order, item.RuleID+"/"+item.InstrumentID)
	}
	if fmt.Sprint(order) != "[b/A b/B b/C a/A a/B a/C]" {
		t.Fatalf("解释明细顺序不符：%v", order)
	}
	second := evaluate(t, program, frame, State{})
	if fingerprint(t, first) != fingerprint(t, second) {
		t.Fatal("config_error 的两次求值结果不同")
	}
}
