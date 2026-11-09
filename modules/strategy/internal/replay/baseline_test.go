package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/stretchr/testify/require"
)

// 基线金样（第 0 期）：在现货金样（spot_golden.json）之外补永续、双市场（两个任务）与统一数据集（一个任务）的研究回放，
// 包含 universe 的标签与黑白名单、规则的显式 pool、min_age_bars（双市场按市场分别判年龄）与永续逐期结算。
// 每份金样导出每期的目标、账本（持仓快照与本期结算摘要）、权益与各市场市值，以及最终绩效，存为 testdata/baseline/<场景>.json。
// 金样由当前代码生成，默认只比对；有意改变结果时用 UPDATE_BASELINE=1 重新生成（不会改动 spot_golden.json）。

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

// baselineSubjects 是基线场景里每个数据集绑定的标的。
var baselineSubjects = []string{"A", "B", "C", "D", "E"}

// baselineClient 在模拟客户端外加上标签成员与五个标的的绑定：标签 majors = A、B、C，meme = C、E。
type baselineClient struct {
	input.Client
	tags map[string][]string
}

func newBaselineClient(inner input.Client) *baselineClient {
	return &baselineClient{Client: inner, tags: map[string][]string{"majors": {"A", "B", "C"}, "meme": {"C", "E"}}}
}

func (c *baselineClient) ListDatasetSubjects(context.Context, string, string) ([]input.Subject, error) {
	subjects := make([]input.Subject, 0, len(baselineSubjects))
	for _, id := range baselineSubjects {
		subjects = append(subjects, input.Subject{SubjectID: id, Active: true})
	}
	return subjects, nil
}

func (c *baselineClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	if members, ok := c.tags[tagID]; ok {
		return append([]string(nil), members...), nil
	}
	return c.Client.ListTagMembers(ctx, spaceID, tagID)
}

func (c *baselineClient) GetTag(ctx context.Context, spaceID, tagID string) (input.TagInfo, error) {
	if _, ok := c.tags[tagID]; ok {
		return input.TagInfo{}, nil
	}
	return c.Client.GetTag(ctx, spaceID, tagID)
}

// baselinePrice 是现货的合成收盘价：B 的动量多数时候最高，C 其次，A 稳步上涨，D 小幅波动，E 多数时候下跌、隔几根反弹。
func baselinePrice(id string, hour int) float64 {
	switch id {
	case "A":
		return 100 + float64(hour)
	case "B":
		return 50 + float64(hour%5)*5
	case "C":
		return 30 + float64(hour%4)*4
	case "D":
		return 20 + float64(hour%6)
	default:
		return 40 - float64(hour%7)
	}
}

// baselineSwapPrice 是永续的合成收盘价：与现货错开两根、水平高 1，价格表混用时结果会对不上。
func baselineSwapPrice(id string, hour int) float64 { return baselinePrice(id, hour+2) + 1 }

// baselineDualClient 返回现货、永续各一个 View 的模拟客户端：永续的 B 第 8 小时才上线，现货的 D 第 6 小时才上线，
// 永续的 A 在第 9、10 小时缺两根（持仓冻结、恢复后从冻结前的价格结算）。
func baselineDualClient() *dualClient {
	dual := &dualClient{
		spot: &fakeClient{marketType: "spot", priceFn: baselinePrice},
		swap: &fakeClient{marketType: "swap", viewName: "view_factor_b", datasetName: "ds_swap", priceFn: baselineSwapPrice},
	}
	dual.spot.missing = map[string]map[int]bool{"D": baselineHours(0, 5)}
	dual.swap.missing = map[string]map[int]bool{"B": baselineHours(0, 7), "A": baselineHours(9, 10)}
	return dual
}

// baselineSwapClient 是纯永续场景的客户端：在 baselineDualClient 之外，永续的 D 第 7 小时才上线（新上市，年龄不够的几根被剔除）。
func baselineSwapClient() *dualClient {
	dual := baselineDualClient()
	dual.swap.missing["D"] = baselineHours(0, 6)
	return dual
}

// baselineHours 返回 [from, to] 闭区间的小时集合。
func baselineHours(from, to int) map[int]bool {
	out := map[int]bool{}
	for hour := from; hour <= to; hour++ {
		out[hour] = true
	}
	return out
}

// baselineSwapDSL：纯永续，universe 白名单是 majors 标签加 D，排除 meme 标签与 B（B、C 的动量最高，都被排除）；
// 新上线的标的要满 3 根；signal 规则的显式 pool 只有 E（替代 universe，不受黑名单影响）。
const baselineSwapDSL = `name: baseline_swap
market: {select: swap}
universe:
  tags: [majors]
  include: [D]
  exclude_tags: [meme]
  exclude: [B]
  min_age_bars: 3
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1.5}
  - id: e
    type: signal
    pool: [E]
    entry: "mom > 0"
    exit: "mom < 0"
    weight: {total: 0.5}
portfolio:
  leverage: 2
  max_missing: 1
`

// baselineDualDSL：两边选、永续优先，多头取动量最高、空头取动量最低；年龄按市场分别判断，排除 E。
const baselineDualDSL = `name: baseline_dual
market: {select: both, prefer: swap}
universe:
  exclude: [E]
  min_age_bars: 3
rules:
  - id: l
    type: rank
    score: "mom"
    select: {top: 1}
    weight: 0.6
  - id: s
    type: rank
    side: short
    score: "-mom"
    select: {top: 1}
    weight: 0.4
portfolio:
  leverage: 2
  max_missing: 1
`

type baselineReplayScenario struct {
	name   string
	dsl    string
	client func() input.Client
	tasks  map[string]store.ReplayTask
	hours  int
	// check 是与金样无关的硬断言：确认场景标注的特性真的发生了。
	check func(t *testing.T, bars []store.ReplayBar)
}

func baselineReplayScenarios() []baselineReplayScenario {
	return []baselineReplayScenario{
		{name: "swap_universe_pool", dsl: baselineSwapDSL, hours: 16,
			client: func() input.Client { return newBaselineClient(baselineSwapClient()) },
			tasks:  map[string]store.ReplayTask{"swap": swapTask}, check: baselineCheckSwap},
		{name: "dual_two_tasks", dsl: baselineDualDSL, hours: 16,
			client: func() input.Client { return newBaselineClient(baselineDualClient()) },
			tasks:  map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, check: baselineCheckDual},
		{name: "dual_unified", dsl: baselineDualDSL, hours: 16,
			client: func() input.Client { return newBaselineClient(&unifiedClient{dualClient: baselineDualClient()}) },
			tasks:  map[string]store.ReplayTask{"multi": uniTask}, check: baselineCheckDual},
	}
}

// baselineCheckSwap：B、C 被黑名单与排除标签挡住；白名单 include 的 D 与显式 pool 的 E 入选过；D 新上市的几期被年龄剔除；
// 永续 A 缺价时持仓冻结；永续持仓逐期结算。
func baselineCheckSwap(t *testing.T, bars []store.ReplayBar) {
	t.Helper()
	sawD, sawE := false, false
	for hour, targets := range baselineTargetsByHour(t, bars) {
		for key := range targets {
			require.True(t, strings.HasSuffix(key, "|swap"), "纯永续回放只有永续目标：%s", key)
			require.False(t, strings.HasPrefix(key, "B|") || strings.HasPrefix(key, "C|"), "第 %d 小时 B、C 被黑名单与排除标签挡住，不应入选：%v", hour, targets)
			sawD = sawD || key == "D|swap"
			sawE = sawE || key == "E|swap"
		}
	}
	require.True(t, sawD && sawE, "白名单 include 的 D 与显式 pool 的 E 都应入选过")
	agedOut, frozen, settled := false, false, false
	for i, bar := range bars {
		var summary struct {
			Decision struct {
				Rules map[string]struct {
					AgedOut int `json:"aged_out"`
				} `json:"rules"`
			} `json:"decision"`
		}
		require.NoError(t, json.Unmarshal(bar.SummaryJSON, &summary))
		agedOut = agedOut || summary.Decision.Rules["r"].AgedOut > 0
		frozen = frozen || bar.Frozen > 0
		if i > 0 && bar.SwapValue != 0 && bar.SwapValue != bars[i-1].SwapValue {
			settled = true
		}
	}
	require.True(t, agedOut, "永续新上市的 D 在年龄不够的几期应被剔除")
	require.True(t, frozen, "永续 A 缺价的两根应冻结持仓")
	require.True(t, settled, "永续持仓应逐期结算（永续名义价值随价格变化）")
}

// baselineCheckDual：B 的永续第 10 小时才满足年龄，之前不能用永续的行入选，但可以用现货的行入选（年龄按市场判断）。
func baselineCheckDual(t *testing.T, bars []store.ReplayBar) {
	t.Helper()
	sawBSpot := false
	for hour, targets := range baselineTargetsByHour(t, bars) {
		if hour < 10 {
			_, young := targets["B|swap"]
			require.False(t, young, "第 %d 小时 B 的永续年龄不够，不能入选：%v", hour, targets)
			_, spot := targets["B|spot"]
			sawBSpot = sawBSpot || spot
		}
	}
	require.True(t, sawBSpot, "B 的永续年龄不够时应改用现货的行（年龄按市场判断）")
}

// baselineReplayDump 执行一次回放，取出逐期记录与最终绩效。
func baselineReplayDump(t *testing.T, scenario baselineReplayScenario) (map[string]any, []store.ReplayBar) {
	t.Helper()
	repo := openStore(t)
	client := scenario.client()
	job := startMarketReplay(t, repo, client, "b1", scenario.dsl, scenario.hours, scenario.tasks, 10, 5)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "b1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, "场景 %s 应完成：%s", scenario.name, done.Error)
	decode := func(raw []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		require.NoError(t, decoder.Decode(&value))
		return value
	}
	bars := barsOf(t, repo, "b1")
	out := make([]any, 0, len(bars))
	for _, bar := range bars {
		out = append(out, map[string]any{
			"bar_end": bar.BarEndTime.Unix(), "status": bar.Status, "skip_reason": bar.SkipReason,
			"return": bar.Return, "equity": bar.Equity, "turnover": bar.Turnover, "fee": bar.Fee,
			"spot_value": bar.SpotValue, "swap_value": bar.SwapValue,
			"holdings": bar.Holdings, "frozen": bar.Frozen, "unfilled": bar.Unfilled, "liquidated": bar.Liquidated,
			"targets": decode(bar.TargetsJSON), "positions": decode(bar.PositionsJSON), "summary": decode(bar.SummaryJSON),
		})
	}
	return map[string]any{"dsl": scenario.dsl, "metrics": decode(done.MetricsJSON), "bars": out}, bars
}

// baselineTargetsByHour 返回每期目标（标的|市场 → 权重），键是收盘价所在的小时。
func baselineTargetsByHour(t *testing.T, bars []store.ReplayBar) map[int]map[string]float64 {
	t.Helper()
	out := map[int]map[string]float64{}
	for _, bar := range bars {
		out[hourOf(bar)] = decodeTargets(t, bar)
	}
	return out
}

func TestBaselineReplayGoldens(t *testing.T) {
	results := map[string][]store.ReplayBar{}
	for _, scenario := range baselineReplayScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			dump, bars := baselineReplayDump(t, scenario)
			require.Len(t, bars, scenario.hours)
			scenario.check(t, bars)
			results[scenario.name] = bars
			baselineCompare(t, filepath.Join("testdata", "baseline", scenario.name+".json"), baselineJSON(t, dump))
		})
	}
	// 统一数据集（一个任务）与两个任务读同样的行，逐期目标与账本应相同；只单跑其中一个子测试时不比较。
	two, okTwo := results["dual_two_tasks"]
	unified, okUnified := results["dual_unified"]
	if !okTwo || !okUnified {
		return
	}
	require.Len(t, unified, len(two))
	for i := range two {
		require.Equal(t, string(two[i].TargetsJSON), string(unified[i].TargetsJSON), "第 %d 期：统一数据集与两个任务的目标应一致", i)
		require.Equal(t, string(two[i].PositionsJSON), string(unified[i].PositionsJSON), "第 %d 期：统一数据集与两个任务的账本应一致", i)
	}
}
