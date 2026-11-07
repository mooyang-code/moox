package replay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 纯现货策略的回放结果在引入市场维度前后必须逐位一致。testdata/spot_golden.json 是在账本加市场维度之前（提交 96df353c）
// 用同一批场景生成的：每期的权益、收益、换手、手续费、持仓、目标、持仓账本与摘要，以及最终绩效。
// 加市场维度后唯一允许的差别是新增的字段（持仓的 market、instrument_id，每期的 spot_value、swap_value）
// 与持仓键、未成交、清算清单里多出的 "|spot" 后缀，比较前统一抹平。

const goldenDSLTop2 = `name: replay_top2
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 2}
    weight: {total: 0.8}
portfolio:
  max_missing: 1
`

type goldenScenario struct {
	name   string
	dsl    string
	client func() *fakeClient
	hours  int
	chunk  int
	liq    int
}

func goldenScenarios() []goldenScenario {
	gapA := map[string]map[int]bool{"A": {5: true, 6: true, 7: true, 8: true}}
	return []goldenScenario{
		{name: "top1_missing_price", dsl: replayDSL, client: func() *fakeClient { return &fakeClient{marketType: "spot", missing: gapA} }, hours: 14, chunk: 4, liq: 2},
		{name: "top2_cash_buffer", dsl: goldenDSLTop2, client: func() *fakeClient { return &fakeClient{marketType: "spot"} }, hours: 12, chunk: 5, liq: 3},
		{name: "min_age_new_listing", dsl: strings.Replace(replayDSL, "rules:", "universe:\n  min_age_bars: 3\nrules:", 1), client: func() *fakeClient {
			return &fakeClient{marketType: "spot", missing: map[string]map[int]bool{"B": {0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true}}}
		}, hours: 12, chunk: 4, liq: 3},
		{name: "previous_bar_ambiguous", dsl: previousDSL, client: func() *fakeClient {
			return &fakeClient{marketType: "spot", ambiguous: map[int]bool{6: true}, missing: map[string]map[int]bool{"B": {9: true, 10: true}}}
		}, hours: 12, chunk: 3, liq: 2},
	}
}

// goldenDump 取出一次回放的全部输出：逐期记录与最终绩效。
func goldenDump(t *testing.T, scenario goldenScenario) map[string]any {
	t.Helper()
	repo := openStore(t)
	job := startReplay(t, repo, "g1", scenario.dsl, scenario.hours)
	(&Runner{Store: repo, Client: scenario.client(), ChunkBars: scenario.chunk, MissingPriceLiquidateBars: scenario.liq, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" {
		t.Fatalf("场景 %s 应完成：%+v", scenario.name, done)
	}
	var metrics any
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	var bars []any
	for _, bar := range barsOf(t, repo, "g1") {
		decode := func(raw []byte) any {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		bars = append(bars, map[string]any{
			"bar_end": bar.BarEndTime.Unix(), "status": bar.Status, "skip_reason": bar.SkipReason,
			"return": bar.Return, "equity": bar.Equity, "turnover": bar.Turnover, "fee": bar.Fee,
			"holdings": bar.Holdings, "frozen": bar.Frozen, "unfilled": bar.Unfilled, "liquidated": bar.Liquidated,
			"targets": decode(bar.TargetsJSON), "positions": decode(bar.PositionsJSON), "summary": decode(bar.SummaryJSON),
		})
	}
	return map[string]any{"metrics": metrics, "bars": bars}
}

// goldenNormalize 抹平加市场维度后新增的字段与键后缀，使新旧输出可以直接比较。
func goldenNormalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			switch key {
			case "market", "instrument_id", "spot_value", "swap_value":
				continue
			}
			out[strings.TrimSuffix(key, "|spot")] = goldenNormalize(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = goldenNormalize(item)
		}
		return out
	case string:
		return strings.TrimSuffix(typed, "|spot")
	default:
		return value
	}
}

func TestSpotReplayMatchesPreMarketLedgerGolden(t *testing.T) {
	path := filepath.Join("testdata", "spot_golden.json")
	got := map[string]any{}
	for _, scenario := range goldenScenarios() {
		got[scenario.name] = goldenNormalize(goldenDump(t, scenario))
	}
	if os.Getenv("UPDATE_SPOT_GOLDEN") != "" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	// 经 JSON 往返，把 int 等类型统一成 float64 再比较。
	roundTrip, _ := json.Marshal(got)
	var normalized map[string]any
	if err := json.Unmarshal(roundTrip, &normalized); err != nil {
		t.Fatal(err)
	}
	for name, expected := range want {
		if !reflect.DeepEqual(normalized[name], expected) {
			gotJSON, _ := json.MarshalIndent(normalized[name], "", " ")
			wantJSON, _ := json.MarshalIndent(expected, "", " ")
			t.Fatalf("场景 %s 的回放结果与加市场维度前不一致\n现在：%s\n原来：%s", name, firstDiff(string(gotJSON), 600), firstDiff(string(wantJSON), 600))
		}
	}
	if len(normalized) != len(want) {
		t.Fatalf("场景数不一致：%d / %d", len(normalized), len(want))
	}
}

func firstDiff(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
