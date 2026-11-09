package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 基线金样（第 0 期）：用合成帧多期驱动引擎，导出每期的目标、规则状态（rule_states_json，与实盘写库同样用 json.Marshal(decision.State)）、
// 摘要与解释明细，存为 testdata/baseline/<场景>.json。覆盖 rank 的 holding/offsets、buffer、stop、filter_after，signal，
// 五种市场模式（含市场不可用时的冻结）与统一数据集。金样由当前代码生成，默认只比对；有意改变结果时用 UPDATE_BASELINE=1 重新生成。

// baselineUpdate 报告本次是否重新生成基线金样。
func baselineUpdate() bool { return os.Getenv("UPDATE_BASELINE") == "1" }

// baselineJSON 把值编码成规范的 JSON：对象键排序、两个空格缩进、不转义 HTML 字符，数值保留原文。数组顺序不动（顺序有语义）。
func baselineJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	require.NoError(t, decoder.Decode(&generic))
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	require.NoError(t, encoder.Encode(generic))
	return out.Bytes()
}

// baselineCompare 与金样文件按字节比较；UPDATE_BASELINE=1 时改为写入。
func baselineCompare(t *testing.T, path string, got []byte) {
	t.Helper()
	if baselineUpdate() {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "没有金样 %s：用 UPDATE_BASELINE=1 生成", path)
	if bytes.Equal(want, got) {
		return
	}
	wantLines, gotLines := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(wantLines), len(gotLines)); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			t.Fatalf("%s 与基线金样不一致（第 %d 行）\n金样：%s\n现在：%s", path, i+1, w, g)
		}
	}
	t.Fatalf("%s 与基线金样的字节不一致", path)
}

// baselineEngineBar 是一期的输入：各市场的行、失败证据与年龄不够的标的，以及本期没有行但仍在基础集合里的标的。
// 上一根（bars[-1]）的值取上一期同一市场同一标的的行。
type baselineEngineBar struct {
	Rows    map[string]map[string]map[string]float64 `json:"rows"`
	Failed  map[string]map[string]map[string]string  `json:"failed,omitempty"`
	AgedOut map[string][]string                      `json:"aged_out,omitempty"`
	Listed  []string                                 `json:"listed,omitempty"`
}

type baselineEngineScenario struct {
	name string
	// mode 为零值时用 DSL 写的 market；DSL 也没写时按测试默认的永续单市场。
	mode    dsl.Market
	dsl     string
	columns []string
	bars    []baselineEngineBar
	check   func(t *testing.T, decisions []Decision)
}

var baselineEngineOrigin = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// baselineEngineFrame 构造一期的帧：基础集合是选币市场里有行或有失败证据的标的加 Listed；规则的预期集合是基础集合，
// 显式 pool 的规则取 pool ∩ 基础集合；年龄不够的标的按市场写进 MarketInput.AgedOut。
func baselineEngineFrame(program *dsl.Program, index int64, bar baselineEngineBar, previous *baselineEngineBar) Frame {
	mode := program.Strategy.Market
	frame := Frame{BarIndex: index, BarEnd: baselineEngineOrigin.Add(time.Duration(index) * time.Hour),
		Markets: map[string]MarketInput{}, Expected: map[string][]string{}, AgedOut: map[string][]string{}}
	universe := map[string]struct{}{}
	for _, id := range bar.Listed {
		universe[id] = struct{}{}
	}
	for _, market := range mode.Markets() {
		selecting := mode.Select == dsl.MarketBoth || mode.Select == market
		input := MarketInput{Rows: map[string]Row{}, Failed: bar.Failed[market]}
		for id, values := range bar.Rows[market] {
			row := Row{Values: copyValues(values)}
			if previous != nil {
				if before, ok := previous.Rows[market][id]; ok {
					row.Previous = copyValues(before)
				}
			}
			input.Rows[id] = row
			if selecting {
				universe[id] = struct{}{}
			}
		}
		for _, subjects := range bar.Failed[market] {
			for id := range subjects {
				if selecting {
					universe[id] = struct{}{}
				}
			}
		}
		if ids := bar.AgedOut[market]; len(ids) > 0 {
			input.AgedOut = make(map[string]struct{}, len(ids))
			for _, id := range ids {
				input.AgedOut[id] = struct{}{}
			}
		}
		frame.Markets[market] = input
	}
	for id := range universe {
		frame.Universe = append(frame.Universe, id)
	}
	sort.Strings(frame.Universe)
	for _, rule := range program.Rules {
		expected := frame.Universe
		if rule.Rule.Pool.Explicit {
			expected = nil
			for _, id := range frame.Universe {
				if contains(rule.Rule.Pool.Fixed, id) {
					expected = append(expected, id)
				}
			}
		}
		frame.Expected[rule.Rule.ID] = expected
	}
	return frame
}

func copyValues(values map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

type baselineEngineTarget struct {
	InstrumentID string `json:"instrument_id"`
	Market       string `json:"market"`
	Weight       string `json:"weight"`
	Hold         bool   `json:"hold,omitempty"`
}

type baselineEngineOutput struct {
	BarIndex   int64                  `json:"bar_index"`
	Input      baselineEngineBar      `json:"input"`
	Status     string                 `json:"status"`
	SkipReason string                 `json:"skip_reason,omitempty"`
	Targets    []baselineEngineTarget `json:"targets"`
	RuleStates json.RawMessage        `json:"rule_states_json"`
	Summary    Summary                `json:"summary"`
	Items      []Item                 `json:"items"`
}

// baselineEngineRun 逐期求值一个场景，返回每期的决策与金样内容。每期的前序状态是上一期决策的 State（跳过时等于更前一期的状态）。
func baselineEngineRun(t *testing.T, scenario baselineEngineScenario) ([]Decision, []byte) {
	t.Helper()
	program := compileIn(t, scenario.mode, scenario.dsl, scenario.columns...)
	state := State{}
	var decisions []Decision
	var outputs []baselineEngineOutput
	for i := range scenario.bars {
		var previous *baselineEngineBar
		if i > 0 {
			previous = &scenario.bars[i-1]
		}
		index := int64(100 + i)
		decision := evaluate(t, program, baselineEngineFrame(program, index, scenario.bars[i], previous), state)
		ruleStates, err := json.Marshal(decision.State)
		require.NoError(t, err)
		targets := make([]baselineEngineTarget, 0, len(decision.Targets))
		for _, target := range decision.Targets {
			targets = append(targets, baselineEngineTarget{InstrumentID: target.InstrumentID, Market: target.Market, Weight: target.Weight.String(), Hold: target.Hold})
		}
		outputs = append(outputs, baselineEngineOutput{BarIndex: index, Input: scenario.bars[i], Status: decision.Status, SkipReason: decision.SkipReason,
			Targets: targets, RuleStates: ruleStates, Summary: decision.Summary, Items: decision.Items})
		decisions = append(decisions, decision)
		state = decision.State
	}
	return decisions, baselineJSON(t, map[string]any{"name": scenario.name, "market": program.Strategy.Market.String(), "dsl": scenario.dsl, "bars": outputs})
}

// baselineOne 把单市场的行（标的 → 列 → 值）放进指定市场。
func baselineOne(market string, rows map[string]map[string]float64) map[string]map[string]map[string]float64 {
	return map[string]map[string]map[string]float64{market: rows}
}

// baselineItem 取一期里某规则某标的的明细；没有时返回零值。
func baselineItem(decision Decision, ruleID, id string) Item {
	for _, item := range decision.Items {
		if item.RuleID == ruleID && item.InstrumentID == id {
			return item
		}
	}
	return Item{}
}

func baselineHasItem(decisions []Decision, match func(Item) bool) bool {
	for _, decision := range decisions {
		for _, item := range decision.Items {
			if match(item) {
				return true
			}
		}
	}
	return false
}

func baselineTargetKeys(decision Decision) map[string]string {
	out := map[string]string{}
	for _, target := range decision.Targets {
		weight := target.Weight.String()
		if target.Hold {
			weight += "(hold)"
		}
		out[TargetKey(target.InstrumentID, target.Market)] = weight
	}
	return out
}

// baselineHoldingBars 是 holding 场景的数据：六个标的、八期，分数轮转；liq 为 0 的被 filter 淘汰，ok 为 0 的被 filter_after 剔除；
// 第 4 期 B 没有行（仍在基础集合里），按缺数从批次移除。
func baselineHoldingBars() []baselineEngineBar {
	ids := []string{"A", "B", "C", "D", "E", "F"}
	bars := make([]baselineEngineBar, 8)
	for bar := range bars {
		rows := map[string]map[string]float64{}
		for k, id := range ids {
			if bar == 4 && id == "B" {
				continue
			}
			liq, ok := 1.0, 1.0
			if (bar+k)%5 == 0 {
				liq = 0
			}
			if (bar*2+k)%6 == 1 {
				ok = 0
			}
			rows[id] = map[string]float64{"m": float64((bar*5+k*3)%7) + float64(k)/4, "liq": liq, "ok": ok}
		}
		bars[bar] = baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, rows)}
		if bar == 4 {
			bars[bar].Listed = []string{"B"}
		}
	}
	return bars
}

// baselineBufferBars 是 buffer 场景的数据（分数表逐期写出）：第 0 期没有上一根、filter 引用 bars[-1] 全部缺数，可用标的不够而跳过；
// 第 2 期 B 掉到第 3 名、靠 buffer 留住；第 3 期 B 掉到第 4 名被换掉；第 4 期 A 被 filter_after 剔除；
// 第 5 期 D、E 没有行、缺数超过 max_missing 而跳过；第 6 期 D、E 没有上一根，同样跳过（状态沿用第 4 期）；
// 第 7 期 select.where 淘汰负分的 D，B 相对上一根大跌被 filter 淘汰。
func baselineBufferBars() []baselineEngineBar {
	table := []map[string][2]float64{
		{"A": {5, 1}, "B": {4, 1}, "C": {3, 1}, "D": {2, 1}, "E": {1, 1}},
		{"A": {5, 1}, "B": {4, 1}, "C": {3, 1}, "D": {2, 1}, "E": {1, 1}},
		{"A": {5, 1}, "B": {4, 1}, "C": {4.5, 1}, "D": {2, 1}, "E": {1, 1}},
		{"A": {5, 1}, "B": {4, 1}, "C": {4.5, 1}, "D": {4.25, 1}, "E": {1, 1}},
		{"A": {5, 0}, "B": {4, 1}, "C": {4.5, 1}, "D": {4.25, 1}, "E": {1, 1}},
		{"A": {5, 1}, "B": {4, 1}, "C": {4.5, 1}},
		{"A": {3, 1}, "B": {2, 1}, "C": {4.5, 1}, "D": {-1, 1}, "E": {0.5, 1}},
		{"A": {3, 1}, "B": {-2, 1}, "C": {4.5, 1}, "D": {-1, 1}, "E": {0.5, 1}},
	}
	bars := make([]baselineEngineBar, len(table))
	for i, rows := range table {
		values := map[string]map[string]float64{}
		for id, pair := range rows {
			values[id] = map[string]float64{"m": pair[0], "ok": pair[1]}
		}
		bars[i] = baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, values)}
		if i == 5 {
			bars[i].Listed = []string{"D", "E"}
		}
	}
	return bars
}

// baselineStopBars 是止损止盈场景的数据：A 跌 11% 触发固定止损并冷却 2 根；B 先涨后从最高价回撤超过 15% 触发跟踪止损（extreme 口径看最低价）；
// C 做空后下跌 21% 触发止盈并冷却 1 根；D、E 是补位的候选。
func baselineStopBars() []baselineEngineBar {
	closes := map[string][]float64{
		"A": {100, 95, 89, 90, 92, 95, 96, 97},
		"B": {100, 110, 120, 125, 110, 112, 113, 114},
		"C": {100, 95, 85, 79, 82, 84, 86, 88},
		"D": {50, 50, 51, 51, 52, 52, 53, 53},
		"E": {40, 41, 41, 42, 42, 43, 43, 44},
	}
	scores := map[string][3]float64{"A": {9, 1, 5}, "B": {2, 9, 6}, "C": {3, 2, 1}, "D": {5, 5, 4}, "E": {1, 4, 3}}
	bars := make([]baselineEngineBar, 8)
	for bar := range bars {
		rows := map[string]map[string]float64{}
		for id, series := range closes {
			close := series[bar]
			high, low := close+1, close-1
			if id == "B" && bar == 3 {
				high = 126
			}
			if id == "B" && bar == 4 {
				low = 105
			}
			score := scores[id]
			rows[id] = map[string]float64{"close": close, "high": high, "low": low, "m1": score[0], "m2": score[1], "m3": score[2]}
		}
		bars[bar] = baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, rows)}
	}
	return bars
}

// baselineSignalBars 是 signal 场景的数据：A、B 按 close 上穿 ma 入场、跌破 ma 出场；C 是空头，close 低于 ma 入场，第 4 期相对入场价涨 5% 以上止损（exit 不成立）；
// 第 5 期 C 的 ma 因子失败（缺数）。
func baselineSignalBars() []baselineEngineBar {
	closes := map[string][]float64{
		"A": {99, 101, 103, 98, 97, 102, 104, 103},
		"B": {50, 49, 48, 51, 52, 53, 49, 50},
		"C": {20, 19, 18, 19, 20, 20, 19.5, 18},
	}
	mas := map[string][]float64{
		"A": {100, 100, 100, 100, 100, 100, 100, 100},
		"B": {50, 50, 50, 50, 50, 50, 50, 50},
		"C": {19.5, 19.5, 19.5, 19.5, 20.5, 19.5, 19.5, 19.5},
	}
	bars := make([]baselineEngineBar, 8)
	for bar := range bars {
		rows := map[string]map[string]float64{}
		for id, series := range closes {
			values := map[string]float64{"close": series[bar], "ma": mas[id][bar]}
			if id == "C" && bar == 5 {
				delete(values, "ma")
			}
			rows[id] = values
		}
		bars[bar] = baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, rows)}
		if bar == 5 {
			bars[bar].Failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"ma": {"C": FailedFactor}}}
		}
	}
	return bars
}

// baselineMarketBars 是五种市场模式共用的数据：现货有 A～D，永续有 A、B、C、E（D 只在现货、E 只在永续），两个市场的分数不同；
// 第 2 期永续上 B 因子失败；第 3 期起分数换位，检验持仓粘在原市场。
func baselineMarketBars() []baselineEngineBar {
	spot := [][]float64{{9, 7, 5, 3}, {9, 7, 5, 8}, {6, 7, 9, 8}, {2, 7, 9, 8}, {2, 3, 9, 8}}
	swap := [][]float64{{1, 8, 6, 10}, {1, 8, 6, 2}, {9, 8, 6, 2}, {9, 8, 1, 3}, {9, 2, 1, 3}}
	bars := make([]baselineEngineBar, len(spot))
	for bar := range bars {
		spotRows := map[string]map[string]float64{}
		for k, id := range []string{"A", "B", "C", "D"} {
			spotRows[id] = map[string]float64{"m": spot[bar][k]}
		}
		swapRows := map[string]map[string]float64{}
		for k, id := range []string{"A", "B", "C", "E"} {
			if bar == 2 && id == "B" {
				continue
			}
			swapRows[id] = map[string]float64{"m": swap[bar][k]}
		}
		bars[bar] = baselineEngineBar{Rows: map[string]map[string]map[string]float64{dsl.MarketSpot: spotRows, dsl.MarketSwap: swapRows}}
		if bar == 2 {
			bars[bar].Failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"m": {"B": FailedFactor}}}
		}
	}
	return bars
}

// baselineFreezeBars 是市场不可用场景的数据：前两期两个市场都正常；第 2、3 期永续整体不可用（行清空，基础集合的每个标的都带 market_unavailable）；
// 第 4 期永续恢复。
func baselineFreezeBars() []baselineEngineBar {
	spot := [][]float64{{1, 2, 3, 9}, {1, 2, 3, 9}, {9, 2, 3, 1}, {9, 2, 3, 1}, {9, 2, 3, 1}}
	swap := [][]float64{{1, 2, 3, 9}, {1, 2, 3, 9}, nil, nil, {9, 2, 1, 3}}
	ids := []string{"A", "B", "C", "D"}
	bars := make([]baselineEngineBar, len(spot))
	for bar := range bars {
		spotRows, swapRows := map[string]map[string]float64{}, map[string]map[string]float64{}
		for k, id := range ids {
			spotRows[id] = map[string]float64{"m": spot[bar][k]}
			if swap[bar] != nil {
				swapRows[id] = map[string]float64{"m": swap[bar][k]}
			}
		}
		bars[bar] = baselineEngineBar{Rows: map[string]map[string]map[string]float64{dsl.MarketSpot: spotRows, dsl.MarketSwap: swapRows}}
		if swap[bar] == nil {
			down := map[string]string{}
			for _, id := range ids {
				down[id] = FailedMarket
			}
			bars[bar].Failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"m": down}}
		}
	}
	return bars
}

// 统一数据集的 series_tag：同一个数据集里现货、永续各一条序列，按 tag 的 market 段拆进两个市场。
const (
	baselineSpotTag = "venue:binance|market:spot|source:spot_http"
	baselineSwapTag = "venue:binance|market:swap|source:swap_http"
)

type baselineTaggedRow struct {
	subject, tag string
	values       map[string]float64
}

// baselineUnifiedBars 是统一数据集场景的数据：S1～S4 各有现货、永续两条序列，S4 的永续新上市（前 3 期年龄不够）；
// 第 3 期 S3 的永续序列取不到（source_failed），第 5 期 S1 的现货序列取不到。行按 seriesset.SeriesTagMarket 拆进两个市场。
func baselineUnifiedBars() []baselineEngineBar {
	spot := [][]float64{{4, 3, 2, 1}, {4, 3, 2, 1}, {1, 3, 2, 4}, {1, 3, 2, 4}, {1, 2, 4, 3}, {3, 2, 4, 1}}
	swap := [][]float64{{1, 2, 3, 9}, {2, 1, 3, 9}, {2, 1, 3, 9}, {4, 1, 3, 9}, {4, 1, 2, 9}, {4, 3, 2, 1}}
	subjects := []string{"S1", "S2", "S3", "S4"}
	bars := make([]baselineEngineBar, len(spot))
	for bar := range bars {
		var tagged []baselineTaggedRow
		failed := map[string]map[string]map[string]string{}
		for k, subject := range subjects {
			if bar == 5 && subject == "S1" {
				failed[dsl.MarketSpot] = map[string]map[string]string{"m": {subject: FailedSource}}
			} else {
				tagged = append(tagged, baselineTaggedRow{subject: subject, tag: baselineSpotTag, values: map[string]float64{"m": spot[bar][k]}})
			}
			if bar == 3 && subject == "S3" {
				failed[dsl.MarketSwap] = map[string]map[string]string{"m": {subject: FailedSource}}
			} else {
				tagged = append(tagged, baselineTaggedRow{subject: subject, tag: baselineSwapTag, values: map[string]float64{"m": swap[bar][k]}})
			}
		}
		rows := map[string]map[string]map[string]float64{}
		for _, row := range tagged {
			market := seriesset.SeriesTagMarket(row.tag)
			if rows[market] == nil {
				rows[market] = map[string]map[string]float64{}
			}
			rows[market][row.subject] = row.values
		}
		bars[bar] = baselineEngineBar{Rows: rows}
		if len(failed) > 0 {
			bars[bar].Failed = failed
		}
		if bar < 3 {
			bars[bar].AgedOut = map[string][]string{dsl.MarketSwap: {"S4"}}
		}
	}
	return bars
}

const baselineMarketDSL = `name: baseline_market
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const baselineHedgeDSL = `name: baseline_hedge
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: s
    type: rank
    side: short
    score: "m"
    select: {bottom: 1}
    weight: {total: 0.5}
portfolio:
  leverage: 2
  max_missing: 1
`

func baselineEngineScenarios() []baselineEngineScenario {
	scenarios := []baselineEngineScenario{
		{
			name: "rank_holding",
			dsl: `name: baseline_rank_holding
rules:
  - id: h
    type: rank
    filter: "liq > 0"
    score: "m"
    select: {top: 2}
    weight: {total: 0.9, method: rank}
    filter_after: "ok > 0"
    holding: {bars: 3, offsets: [0, 2]}
portfolio:
  max_missing: 1
`,
			columns: []string{"m", "liq", "ok"},
			bars:    baselineHoldingBars(),
			check: func(t *testing.T, decisions []Decision) {
				offsets := map[int]bool{}
				for _, decision := range decisions {
					for _, batch := range decision.State.Rules["h"].Batches {
						offsets[batch.Offset] = true
					}
				}
				require.Equal(t, map[int]bool{0: true, 2: true}, offsets, "两个 offset 都应建立批次")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.Reason == "filter_after" }), "应有 filter_after 剔除")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.Reason == "holding:filter" }), "应有延续批次里被 filter 淘汰仍持有的标的")
				require.Equal(t, StageMissing, baselineItem(decisions[4], "h", "B").Stage, "第 4 期 B 没有行，应记缺数")
			},
		},
		{
			name: "rank_buffer",
			dsl: `name: baseline_rank_buffer
rules:
  - id: r
    type: rank
    filter: "m > bars[-1].m - 3"
    score: "m"
    select: {top: 2, buffer: 1, where: "score > 0"}
    weight: {total: 0.9, method: rank, cap: 0.5}
    filter_after: "ok > 0"
portfolio:
  max_missing: 0.25
  min_universe: 3
`,
			columns: []string{"m", "ok"},
			bars:    baselineBufferBars(),
			check: func(t *testing.T, decisions []Decision) {
				require.Equal(t, SkipUniverseTooSmall, decisions[0].SkipReason, "第 0 期没有上一根，全部缺数、可用标的少于 min_universe，应跳过")
				require.Equal(t, SkipTooManyMissing, decisions[5].SkipReason, "第 5 期两个标的没有行，应因缺数跳过")
				held := baselineItem(decisions[2], "r", "B")
				require.True(t, held.Stage == StageWeighted && held.Rank == 3, "第 2 期 B 排第 3 名，应靠 buffer 留住：%+v", held)
				require.NotEqual(t, StageWeighted, baselineItem(decisions[3], "r", "B").Stage, "第 3 期 B 排第 4 名，应被换掉")
				require.Equal(t, "filter_after", baselineItem(decisions[4], "r", "A").Reason)
				require.Equal(t, SkipTooManyMissing, decisions[6].SkipReason, "第 6 期 D、E 没有上一根，应因缺数跳过")
				require.Equal(t, "select.where", baselineItem(decisions[7], "r", "D").Reason)
				require.Equal(t, StageFiltered, baselineItem(decisions[7], "r", "B").Stage)
			},
		},
		{
			name: "rank_stop",
			dsl: `name: baseline_rank_stop
rules:
  - id: loss
    type: rank
    score: "m1"
    select: {top: 1}
    weight: {total: 0.3}
    stop: {loss: 0.1, cooldown_bars: 2}
  - id: trail
    type: rank
    score: "m2"
    select: {top: 1}
    weight: {total: 0.3}
    stop: {trailing: 0.15, price: extreme}
  - id: profit
    type: rank
    side: short
    score: "m3"
    select: {bottom: 1}
    weight: {total: 0.3}
    stop: {profit: 0.2, cooldown_bars: 1}
portfolio:
  leverage: 1
  max_missing: 1
`,
			columns: []string{"close", "high", "low", "m1", "m2", "m3"},
			bars:    baselineStopBars(),
			check: func(t *testing.T, decisions []Decision) {
				var loss, trailing, profit int
				for _, decision := range decisions {
					loss += decision.Summary.Rules["loss"].StopLoss
					trailing += decision.Summary.Rules["trail"].TrailingStop
					profit += decision.Summary.Rules["profit"].TakeProfit
				}
				require.Positive(t, loss, "应触发固定止损")
				require.Positive(t, trailing, "应触发跟踪止损")
				require.Positive(t, profit, "应触发止盈")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "loss" && item.Reason == reasonCooldown }), "固定止损后应进入冷却")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "profit" && item.Reason == reasonCooldown }), "止盈后应进入冷却")
			},
		},
		{
			name: "signal",
			dsl: `name: baseline_signal
rules:
  - id: long
    type: signal
    pool: [A, B]
    entry: "bars[-1].close <= bars[-1].ma && close > ma"
    exit: "close < ma"
    weight: {total: 0.4}
  - id: short
    type: signal
    pool: [C]
    side: short
    entry: "close < ma"
    exit: "close > ma"
    weight: {total: 0.3}
    stop: {loss: 0.05, cooldown_bars: 1}
portfolio:
  max_missing: 0.5
`,
			columns: []string{"close", "ma"},
			bars:    baselineSignalBars(),
			check: func(t *testing.T, decisions []Decision) {
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "long" && item.Stage == StageWeighted }), "多头应入场")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "long" && item.Stage == StageDropped }), "多头应出场")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "short" && item.Stage == StageWeighted }), "空头应入场")
				require.True(t, baselineHasItem(decisions, func(item Item) bool { return item.RuleID == "short" && item.Stage == StageMissing }), "第 5 期 C 的 ma 缺失")
				require.Equal(t, 1, decisions[4].Summary.Rules["short"].StopLoss, "第 4 期空头 C 应止损")
			},
		},
	}
	modes := []struct {
		name string
		mode dsl.Market
	}{
		{"market_spot_spot", dsl.SingleMarket(dsl.MarketSpot)},
		{"market_swap_swap", dsl.SingleMarket(dsl.MarketSwap)},
		{"market_both_spot", dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}},
		{"market_both_swap", dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}},
		{"market_spot_swap", dsl.Market{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap}},
	}
	for _, item := range modes {
		mode := item.mode
		scenarios = append(scenarios, baselineEngineScenario{name: item.name, mode: mode, dsl: baselineMarketDSL, columns: []string{"m"},
			bars: baselineMarketBars(), check: func(t *testing.T, decisions []Decision) {
				for i, decision := range decisions {
					require.Equal(t, StatusOK, decision.Status, "第 %d 期", i)
					for _, target := range decision.Targets {
						require.True(t, mode.Has(target.Market), "第 %d 期目标的市场 %s 不在模式 %s 里", i, target.Market, mode)
					}
				}
			}})
	}
	freeze := func(t *testing.T, decisions []Decision) {
		require.True(t, baselineHasItem(decisions[2:4], func(item Item) bool { return item.Stage == StageHeld && item.Reason == reasonMarketHold }), "永续不可用时持仓应冻结")
		hold := false
		for _, decision := range decisions[2:4] {
			for _, target := range decision.Targets {
				hold = hold || (target.Hold && target.Market == dsl.MarketSwap)
			}
		}
		require.True(t, hold, "冻结的永续持仓应带保持标记")
	}
	scenarios = append(scenarios,
		baselineEngineScenario{name: "market_freeze_both_spot", mode: dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, dsl: baselineHedgeDSL,
			columns: []string{"m"}, bars: baselineFreezeBars(), check: freeze},
		baselineEngineScenario{name: "market_freeze_spot_swap", mode: dsl.Market{Select: dsl.MarketSpot, Prefer: dsl.MarketSwap}, dsl: baselineMarketDSL,
			columns: []string{"m"}, bars: baselineFreezeBars(), check: freeze},
		baselineEngineScenario{name: "unified", dsl: `name: baseline_unified
market: {select: both, prefer: swap}
rules:
  - id: l
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 0.6}
  - id: s
    type: rank
    side: short
    score: "m"
    select: {bottom: 1}
    weight: {total: 0.4}
portfolio:
  leverage: 2
  max_missing: 1
`, columns: []string{"m"}, bars: baselineUnifiedBars(), check: func(t *testing.T, decisions []Decision) {
			for i, decision := range decisions[:3] {
				_, swapHeld := baselineTargetKeys(decision)[TargetKey("S4", dsl.MarketSwap)]
				require.False(t, swapHeld, "第 %d 期 S4 的永续年龄不够，不能用永续的行入选", i)
			}
			markets := map[string]bool{}
			for _, decision := range decisions {
				for _, target := range decision.Targets {
					markets[target.Market] = true
				}
			}
			require.True(t, markets[dsl.MarketSpot] && markets[dsl.MarketSwap], "统一数据集的两个市场都应出现目标：%v", markets)
		}},
	)
	return scenarios
}

func TestBaselineEngineGoldens(t *testing.T) {
	for _, scenario := range baselineEngineScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			decisions, got := baselineEngineRun(t, scenario)
			_, again := baselineEngineRun(t, scenario)
			require.Equal(t, string(got), string(again), "同一输入两次求值的结果不同")
			scenario.check(t, decisions)
			baselineCompare(t, filepath.Join("testdata", "baseline", scenario.name+".json"), got)
		})
	}
}
