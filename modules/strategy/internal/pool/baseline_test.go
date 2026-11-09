package pool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/stretchr/testify/require"
)

// 基线金样（第 0 期）：用合成成员结果多期驱动策略池，导出每期的池目标、pool.State 的 JSON（engine.State.Pool）、
// 完整摘要（含 summary_json.pool），存为 testdata/baseline/<场景>.json。覆盖 fixed、rotation 的三种因子（及多因子加权）、
// timing、step、成员没通过沿用上期目标（含全部没通过时池跳过）与成员爆仓。金样由当前代码生成，默认只比对；
// 有意改变结果时用 UPDATE_BASELINE=1 重新生成。

// baselineUpdate 报告本次是否重新生成基线金样。
func baselineUpdate() bool { return os.Getenv("UPDATE_BASELINE") != "" }

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

// baselinePoolMember 是一个成员一期的合成结果：Reason 非空表示没通过，否则 Targets 是目标（标的|市场 → 权重），Hold 是带保持标记的键。
type baselinePoolMember struct {
	ID      string            `json:"id"`
	Targets map[string]string `json:"targets,omitempty"`
	Hold    []string          `json:"hold,omitempty"`
	Reason  string            `json:"reason,omitempty"`
}

type baselinePoolBar struct {
	BarIndex int64                `json:"bar_index"`
	Prices   map[string]float64   `json:"prices"`
	Members  []baselinePoolMember `json:"members"`
}

// result 把合成结果转成 MemberResult：目标按键排序，保证输入本身没有 map 顺序。
func (m baselinePoolMember) result() MemberResult {
	if m.Reason != "" {
		return MemberResult{ID: m.ID, Reason: m.Reason, Detail: "合成：本期没通过"}
	}
	keys := make([]string, 0, len(m.Targets))
	for key := range m.Targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := engine.State{Targets: map[string]string{}}
	targets := make([]engine.Target, 0, len(keys))
	for _, key := range keys {
		instrument, market := engine.SplitTargetKey(key)
		hold := false
		for _, held := range m.Hold {
			hold = hold || held == key
		}
		targets = append(targets, engine.Target{InstrumentID: instrument, Market: market, Weight: quant.Must(m.Targets[key]), Hold: hold})
		state.Targets[key] = m.Targets[key]
	}
	return MemberResult{ID: m.ID, Decision: &engine.Decision{Status: engine.StatusOK, Targets: targets, State: state}}
}

type baselinePoolScenario struct {
	name string
	// spec 是 strategy_pool 里 members 之后的部分（allocation、timing、step、fees），portfolio 是组合约束。
	spec, portfolio string
	members         []string
	mode            dsl.Market
	bars            []baselinePoolBar
	check           func(t *testing.T, decisions []engine.Decision)
}

func (s baselinePoolScenario) dsl() string {
	var list strings.Builder
	for _, id := range s.members {
		fmt.Fprintf(&list, "    - {id: %s, strategy: s_%s}\n", id, id)
	}
	return "name: baseline_pool\nbar: 1h\nstrategy_pool:\n  members:\n" + list.String() + s.spec + s.portfolio
}

func (s baselinePoolScenario) config(t *testing.T) Config {
	t.Helper()
	strategy, err := dsl.Parse([]byte(s.dsl()))
	require.NoError(t, err, "解析池失败：\n%s", s.dsl())
	cfg, err := NewConfig(strategy, s.mode)
	require.NoError(t, err)
	return cfg
}

type baselinePoolTarget struct {
	InstrumentID string `json:"instrument_id"`
	Market       string `json:"market"`
	Weight       string `json:"weight"`
	Hold         bool   `json:"hold,omitempty"`
}

type baselinePoolOutput struct {
	BarIndex     int64                `json:"bar_index"`
	Input        baselinePoolBar      `json:"input"`
	Status       string               `json:"status"`
	SkipReason   string               `json:"skip_reason,omitempty"`
	Targets      []baselinePoolTarget `json:"targets"`
	StateTargets map[string]string    `json:"state_targets,omitempty"`
	PoolState    json.RawMessage      `json:"pool_state"`
	Summary      engine.Summary       `json:"summary"`
}

// baselinePoolRun 逐期推进一个场景，返回每期的决策与金样内容。
func baselinePoolRun(t *testing.T, scenario baselinePoolScenario) ([]engine.Decision, []byte) {
	t.Helper()
	cfg := scenario.config(t)
	state := engine.State{}
	var decisions []engine.Decision
	var outputs []baselinePoolOutput
	for _, bar := range scenario.bars {
		members := make([]MemberResult, 0, len(bar.Members))
		for _, member := range bar.Members {
			members = append(members, member.result())
		}
		decision, err := Step(cfg, state, Input{BarIndex: bar.BarIndex, Prices: bar.Prices, Members: members})
		require.NoError(t, err, "第 %d 期", bar.BarIndex)
		targets := make([]baselinePoolTarget, 0, len(decision.Targets))
		for _, target := range decision.Targets {
			targets = append(targets, baselinePoolTarget{InstrumentID: target.InstrumentID, Market: target.Market, Weight: target.Weight.String(), Hold: target.Hold})
		}
		pool := decision.State.Pool
		if len(pool) == 0 {
			pool = json.RawMessage("null")
		}
		outputs = append(outputs, baselinePoolOutput{BarIndex: bar.BarIndex, Input: bar, Status: decision.Status, SkipReason: decision.SkipReason,
			Targets: targets, StateTargets: decision.State.Targets, PoolState: pool, Summary: decision.Summary})
		decisions = append(decisions, decision)
		state = decision.State
	}
	return decisions, baselineJSON(t, map[string]any{"name": scenario.name, "dsl": scenario.dsl(), "market": scenario.mode.String(), "bars": outputs})
}

// baselinePrices 是合成收盘价：X、Y、W 是现货，Z 是永续，逐期写出。
var baselinePrices = map[string][]float64{
	"X|spot": {100, 104, 108, 103, 99, 102, 110, 115, 112},
	"Y|spot": {50, 49, 51, 53, 55, 54, 52, 50, 51},
	"W|spot": {10, 10, 11, 12, 11, 10, 9, 10, 11},
	"Z|swap": {20, 21, 19, 18, 20, 22, 23, 21, 20},
}

func baselinePricesAt(bar int) map[string]float64 {
	out := make(map[string]float64, len(baselinePrices))
	for key, series := range baselinePrices {
		out[key] = series[bar]
	}
	return out
}

// baselineBars 生成 n 期输入：member(i) 给出第 i 期各成员的结果；bar 序号从 1 起连续。
func baselineBars(n int, member func(i int) []baselinePoolMember) []baselinePoolBar {
	bars := make([]baselinePoolBar, n)
	for i := range bars {
		bars[i] = baselinePoolBar{BarIndex: int64(i + 1), Prices: baselinePricesAt(i), Members: member(i)}
	}
	return bars
}

// baselineSteady 是三个成员各自固定的目标：a 满仓 X，b 八成 Y，c 半仓 W 加半仓 Z 空头。
func baselineSteady(int) []baselinePoolMember {
	return []baselinePoolMember{
		{ID: "a", Targets: map[string]string{"X|spot": "1"}},
		{ID: "b", Targets: map[string]string{"Y|spot": "0.8"}},
		{ID: "c", Targets: map[string]string{"W|spot": "0.5", "Z|swap": "-0.5"}},
	}
}

var baselineBoth = dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}

func baselineRatios(decision engine.Decision) map[string]string {
	out := map[string]string{}
	if decision.Summary.Pool == nil {
		return out
	}
	for _, member := range decision.Summary.Pool.Members {
		out[member.ID] = member.Ratio
	}
	return out
}

// baselineRanked 报告某期是否给出了轮动名次分，且分配不是均分（排名真的起了作用）。
func baselineRanked(decision engine.Decision) bool {
	if decision.Summary.Pool == nil {
		return false
	}
	ratios := map[string]bool{}
	for _, member := range decision.Summary.Pool.Members {
		if member.Rank == nil {
			return false
		}
		ratios[member.Ratio] = true
	}
	return len(ratios) > 1
}

func baselineAnyRanked(t *testing.T, decisions []engine.Decision) {
	t.Helper()
	for _, decision := range decisions {
		if baselineRanked(decision) {
			return
		}
	}
	t.Fatal("轮动因子在任何一期都没有排出名次")
}

func baselinePoolScenarios() []baselinePoolScenario {
	fees := "  fees: {spot_bps: 10, swap_bps: 5}\n"
	rotation := func(top int, factors string) string {
		return fmt.Sprintf("  allocation:\n    method: rotation\n    rotation:\n      top: %d\n      factors:\n%s", top, factors)
	}
	return []baselinePoolScenario{
		{
			name:      "fixed",
			spec:      "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.3, c: 0.4}}\n" + fees,
			portfolio: "portfolio: {leverage: 1, max_weight: 0.45}\n",
			members:   []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(6, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) {
				require.Equal(t, map[string]string{"a": "0.5", "b": "0.3", "c": "0.4"}, baselineRatios(decisions[0]))
				require.Contains(t, strings.Join(decisions[0].Summary.Notes, ";"), "max_weight", "a 的 X 超过 max_weight 应截顶")
			},
		},
		{
			name: "rotation_return", spec: rotation(1, "        - {name: return, window: 2}\n") + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) { baselineAnyRanked(t, decisions) },
		},
		{
			name: "rotation_drawdown", spec: rotation(1, "        - {name: drawdown, window: 3}\n") + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) { baselineAnyRanked(t, decisions) },
		},
		{
			name: "rotation_ma_ratio", spec: rotation(2, "        - {name: ma_ratio, window: 2}\n") + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) { baselineAnyRanked(t, decisions) },
		},
		{
			name:    "rotation_weighted",
			spec:    rotation(2, "        - {name: return, window: 1, weight: 1}\n        - {name: drawdown, window: 2, weight: 2}\n        - {name: ma_ratio, window: 3, weight: 1, order: asc}\n") + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) { baselineAnyRanked(t, decisions) },
		},
		{
			name: "timing", spec: "  allocation: {method: fixed, ratios: {a: 0.4, b: 0.3, c: 0.3}}\n  timing: {window: 3}\n" + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) {
				seen := map[string]bool{}
				for _, decision := range decisions {
					for _, member := range decision.Summary.Pool.Members {
						seen[member.Timing] = true
					}
				}
				require.True(t, seen["0"] && seen["1"], "择时系数应既有 1 也有 0：%v", seen)
			},
		},
		{
			name: "step", spec: rotation(1, "        - {name: return, window: 1}\n") + "  step: 0.2\n" + fees,
			members: []string{"a", "b", "c"}, mode: baselineBoth, bars: baselineBars(9, baselineSteady),
			check: func(t *testing.T, decisions []engine.Decision) {
				limited := false
				for _, decision := range decisions {
					for _, member := range decision.Summary.Pool.Members {
						limited = limited || member.RatioBefore != member.Ratio
					}
				}
				require.True(t, limited, "步长限制应改变至少一个成员的比例")
			},
		},
		{
			name: "member_failed", spec: "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n" + fees,
			members: []string{"a", "b"}, mode: dsl.SingleMarket(dsl.MarketSpot),
			bars: baselineBars(7, func(i int) []baselinePoolMember {
				a := baselinePoolMember{ID: "a", Targets: map[string]string{"X|spot": "0.6"}}
				b := baselinePoolMember{ID: "b", Targets: map[string]string{"Y|spot": "0.4", "W|spot": []string{"0.1", "0.2", "0.3", "0.1", "0.2", "0.3", "0.1"}[i]}}
				switch i {
				case 2, 3:
					b = baselinePoolMember{ID: "b", Reason: "factor_skipped"}
				case 4:
					a, b = baselinePoolMember{ID: "a", Reason: "no_data"}, baselinePoolMember{ID: "b", Reason: "config_error"}
				case 5:
					a.Hold = []string{"X|spot"}
				}
				return []baselinePoolMember{a, b}
			}),
			check: func(t *testing.T, decisions []engine.Decision) {
				b := decisions[2].Summary.Pool.Members[1]
				require.Equal(t, engine.StatusSkipped, b.Status, "第 3 期 b 没通过")
				for _, key := range []string{"Y|spot", "W|spot"} {
					require.Equal(t, decisions[1].State.Members["b"].Targets[key], decisions[2].State.Members["b"].Targets[key], "没通过的成员沿用上期目标：%s", key)
				}
				require.Equal(t, engine.StatusSkipped, decisions[4].Status, "第 5 期全部成员没通过，池跳过")
				hold := false
				for _, target := range decisions[5].Targets {
					hold = hold || target.Hold
				}
				require.True(t, hold, "第 6 期成员冻结的标的在池目标里带保持标记")
			},
		},
		{
			name: "bankrupt", spec: rotation(1, "        - {name: return, window: 1}\n") + fees,
			portfolio: "portfolio: {leverage: 10}\n",
			members:   []string{"swapper", "b"}, mode: baselineBoth,
			bars: baselineBars(6, func(i int) []baselinePoolMember {
				return []baselinePoolMember{
					{ID: "swapper", Targets: map[string]string{"Z|swap": "12"}},
					{ID: "b", Targets: map[string]string{"Y|spot": "1"}},
				}
			}),
			check: func(t *testing.T, decisions []engine.Decision) {
				bankrupt := -1
				for i, decision := range decisions {
					if decision.Summary.Pool.Members[0].Bankrupt && bankrupt < 0 {
						bankrupt = i
					}
				}
				require.GreaterOrEqual(t, bankrupt, 0, "12 倍永续多头在 Z 下跌 9.5%% 时应模拟爆仓")
				for _, decision := range decisions[bankrupt+1:] {
					require.Equal(t, "0", baselineRatios(decision)["swapper"], "爆仓之后不再分配")
				}
			},
		},
	}
}

func TestBaselinePoolGoldens(t *testing.T) {
	for _, scenario := range baselinePoolScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			decisions, got := baselinePoolRun(t, scenario)
			_, again := baselinePoolRun(t, scenario)
			require.Equal(t, string(got), string(again), "同一输入两次推进的结果不同")
			scenario.check(t, decisions)
			baselineCompare(t, filepath.Join("testdata", "baseline", scenario.name+".json"), got)
		})
	}
}
