package replay

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/factorname"
)

// paramsClient 在 fakeClient 的 View 上多一个可变成员 bias__var 与 __params 列：请求了 __params 时，每行带上这个成员这一期
// 实际用的参数 window（按小时在 10 与 30 之间交替）。columns 记下主帧读取请求的列。
type paramsClient struct {
	*fakeClient
	columns [][]string
}

func (c *paramsClient) GetView(ctx context.Context, spaceID, datasetID string) (input.ViewInfo, error) {
	view, err := c.fakeClient.GetView(ctx, spaceID, datasetID)
	view.Columns = append(view.Columns,
		input.ViewColumn{Name: "bias__var", Attributes: map[string]string{"column_kind": "output", "member_key": "bias__var", "factor_id": "bias", "member_kind": "var", "output_name": "bias__var"}},
		input.ViewColumn{Name: factorname.ParamsColumn, Attributes: map[string]string{"column_kind": "params"}})
	return view, err
}

func (c *paramsClient) GetFactor(ctx context.Context, factorID string) (input.FactorInfo, error) {
	if factorID == "bias" {
		return input.FactorInfo{FactorID: "bias", DefinitionHash: "sha256:bias", Params: []input.ParamDecl{{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}}, nil
	}
	return c.fakeClient.GetFactor(ctx, factorID)
}

func (c *paramsClient) QueryRows(ctx context.Context, spaceID string, query input.Query) ([]input.Row, uint64, error) {
	rows, revision, err := c.fakeClient.QueryRows(ctx, spaceID, query)
	if err != nil || !slices.Contains(query.Columns, factorname.ParamsColumn) {
		return rows, revision, err
	}
	c.columns = append(c.columns, query.Columns)
	for i := range rows {
		window := 10.0
		if int(rows[i].DataTime.Sub(origin)/time.Hour)%2 == 0 {
			window = 30
		}
		rows[i].Params = map[string]map[string]float64{"bias__var": {"window": window}}
	}
	return rows, revision, err
}

// 只在 window 大于 20 的期选币：params() 取到 NaN 时一期都选不出来。
const paramsDSL = `name: replay_params
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    filter: params("bias__var", "window") > 20
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// 批 3 T10：策略用了 params() 时，View 回放显式读 __params 列，params() 取到 View 里记的实际参数（原来取到 NaN）。
func TestReplayParamsReadFromView(t *testing.T) {
	repo := openStore(t)
	client := &paramsClient{fakeClient: &fakeClient{marketType: "spot"}}
	job := startMarketReplay(t, repo, client, "params", paramsDSL, 8, map[string]store.ReplayTask{"spot": spotTask}, 0, 0)
	runner := &Runner{Store: repo, Client: client, ChunkBars: 3, InitialEquity: 100}
	runner.Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "params")
	if err != nil || done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v err=%v", done, err)
	}
	if len(client.columns) == 0 {
		t.Fatal("用了 params() 的回放应读 __params 列")
	}
	bars := barsOf(t, repo, "params")
	if len(bars) != 8 {
		t.Fatalf("回放周期不符：%d", len(bars))
	}
	for _, bar := range bars {
		hour := int(bar.BarEndTime.Sub(origin)/time.Hour) - 1
		targets := decodeTargets(t, bar)
		if hour%2 == 0 && len(targets) != 1 {
			t.Fatalf("第 %d 小时 window=30，应选出一个标的：%s", hour, bar.TargetsJSON)
		}
		if hour%2 != 0 && len(targets) != 0 {
			t.Fatalf("第 %d 小时 window=10，不应选币：%s", hour, bar.TargetsJSON)
		}
	}
	// 没用 params() 的策略不读 __params，查询与改造前相同。
	plain := &paramsClient{fakeClient: &fakeClient{marketType: "spot"}}
	other := startMarketReplay(t, repo, plain, "plain", replayDSL, 4, map[string]store.ReplayTask{"spot": spotTask}, 0, 0)
	(&Runner{Store: repo, Client: plain, ChunkBars: 3, InitialEquity: 100}).Execute(context.Background(), other)
	if len(plain.columns) != 0 {
		t.Fatalf("没用 params() 的回放不应读 __params：%v", plain.columns)
	}
}

// 回放任务的 slippage_bps 传到账本：同一策略加了滑点后权益更低，逐期摘要带上滑点成本，局限说明写明滑点。
func TestReplaySlippageReachesLedger(t *testing.T) {
	repo := openStore(t)
	run := func(id string, slippage float64) (Metrics, []store.ReplayBar) {
		client := &fakeClient{marketType: "spot"}
		job := store.Replay{ReplayID: id, DSLYaml: replayDSL, DSLHash: "h", Calendar: "crypto_24x7", SpaceID: "crypto", Tasks: map[string]store.ReplayTask{"spot": spotTask},
			StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(10 * time.Hour), SpotFeeBps: 10, SlippageBps: slippage, Factors: pinnedFactors(t, replayDSL), CreatedAt: origin}
		if err := repo.CreateReplay(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
		if err != nil || !found || claimed.SlippageBps != slippage {
			t.Fatalf("认领回放失败：%+v found=%v err=%v", claimed, found, err)
		}
		(&Runner{Store: repo, Client: client, ChunkBars: 3, InitialEquity: 100}).Execute(context.Background(), claimed)
		done, err := repo.GetReplay(context.Background(), id)
		if err != nil || done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v err=%v", done, err)
		}
		var metrics Metrics
		if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
			t.Fatal(err)
		}
		return metrics, barsOf(t, repo, id)
	}
	base, baseBars := run("no_slip", 0)
	slipped, slippedBars := run("slip", 20)
	if slipped.FinalEquity >= base.FinalEquity {
		t.Fatalf("加了滑点权益应更低：%v ≥ %v", slipped.FinalEquity, base.FinalEquity)
	}
	if strings.Contains(string(baseBars[0].SummaryJSON), "slippage") || !strings.Contains(string(slippedBars[0].SummaryJSON), `"slippage"`) {
		t.Fatalf("逐期摘要只在有滑点时带滑点成本：%s / %s", baseBars[0].SummaryJSON, slippedBars[0].SummaryJSON)
	}
	if !slices.ContainsFunc(slipped.Limitations, func(note string) bool { return strings.Contains(note, "20 bps 滑点") }) {
		t.Fatalf("局限说明应写明滑点：%v", slipped.Limitations)
	}
}

// 批 3 T10：策略池成员用了 params() 时，启用时的预热（RunPool）与池回放都读 __params，成员取到 View 里记的实际参数（与实时求值成员时一致），
// 只在 window=30 的期出目标，两者逐期一致。
func TestPoolWarmUpAndReplayReadParams(t *testing.T) {
	member := poolMember(t, "pm", "spot", "    filter: 'params(\"bias__var\", \"window\") > 20'\n    select: {top: 1}\n    allow_empty: true\n")
	list := []input.PoolMemberDef{member}
	raw := poolYaml("  allocation: {method: fixed, ratios: {pm: 1}}\n", "", list...)
	repo := openStore(t)
	client := &paramsClient{fakeClient: &fakeClient{marketType: "spot"}}
	job := startPoolReplay(t, repo, client, "pp", raw, list, 10)
	client.columns = nil
	(&Runner{Store: repo, Client: client, ChunkBars: 3, InitialEquity: 1}).Execute(context.Background(), job)
	if done, _ := replayMetrics(t, repo, "pp"); done.Status != store.ReplayDone {
		t.Fatalf("池回放应完成：%+v", done)
	}
	if len(client.columns) == 0 {
		t.Fatal("池回放应读 __params 列")
	}
	warm := &paramsClient{fakeClient: &fakeClient{marketType: "spot"}}
	decisions := warmPool(t, warm, raw, list, 10, 4)
	if len(warm.columns) == 0 {
		t.Fatal("预热应读 __params 列")
	}
	bars := barsOf(t, repo, "pp")
	requireSameAsWarmUp(t, bars, decisions)
	for _, bar := range bars {
		hour := int(bar.BarEndTime.Sub(origin)/time.Hour) - 1
		targets := decodeTargets(t, bar)
		if hour%2 == 0 && len(targets) != 1 {
			t.Fatalf("第 %d 小时 window=30，池应有一个目标：%s", hour, bar.TargetsJSON)
		}
		if hour%2 != 0 && len(targets) != 0 {
			t.Fatalf("第 %d 小时 window=10，池不应有目标：%s", hour, bar.TargetsJSON)
		}
	}
}
