package replay

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/stretchr/testify/require"
)

const slowView = "view_factor_s"

// sourceClient 在现货 1h 的 fakeClient 之外加一个 4h 的来源 View（任务 ftask_s）：列 trend 按标的与 4h 序号交替正负，
// A 的第 2 根 4h（origin+8h 起）没有行。
type sourceClient struct {
	*fakeClient
	sourceQueries []input.Query
}

// C 在实时里是停用的绑定（回放里视为活跃，属于已知差别），它的趋势恒为负，两边都不会选它。
func slowTrend(id string, k int) float64 {
	if id == "C" {
		return -1
	}
	if (k%2 == 0) == (id == "A") {
		return 1
	}
	return -1
}

func (c *sourceClient) GetView(ctx context.Context, spaceID, viewID string) (input.ViewInfo, error) {
	if viewID != slowView {
		return c.fakeClient.GetView(ctx, spaceID, viewID)
	}
	return input.ViewInfo{ViewID: slowView, DatasetID: "ds_s", Frequency: "4h", Status: "active", ActiveIndexID: "idx_s", Generation: "idx_s",
		Columns: []input.ViewColumn{{Name: "close", Attributes: map[string]string{}}, {Name: "trend", Attributes: map[string]string{}}}}, nil
}

func (c *sourceClient) ViewGeneration(ctx context.Context, spaceID, viewID string) (string, error) {
	view, err := c.GetView(ctx, spaceID, viewID)
	return view.Generation, err
}

func (c *sourceClient) GetDataset(ctx context.Context, spaceID, datasetID string) (input.DatasetInfo, error) {
	if datasetID != "ds_s" {
		return c.fakeClient.GetDataset(ctx, spaceID, datasetID)
	}
	return input.DatasetInfo{DatasetID: "ds_s", Status: "active", Frequency: "4h", Retention: "forever", Attributes: map[string]string{"market_type": "spot"}}, nil
}

func (c *sourceClient) QueryRows(ctx context.Context, spaceID string, query input.Query) ([]input.Row, uint64, error) {
	if query.ViewID != slowView {
		return c.fakeClient.QueryRows(ctx, spaceID, query)
	}
	c.sourceQueries = append(c.sourceQueries, query)
	const step = 4 * time.Hour
	var rows []input.Row
	for at := query.Start.Truncate(step); at.Before(query.End); at = at.Add(step) {
		if at.Before(query.Start) {
			continue
		}
		k := int(at.Sub(origin) / step)
		for _, subject := range query.Subjects {
			if subject.SubjectID == "A" && k == 2 {
				continue
			}
			rows = append(rows, input.Row{SubjectID: subject.SubjectID, DataTime: at, SeriesTag: "venue:binance", Values: map[string]float64{"close": 1, "trend": slowTrend(subject.SubjectID, k)}})
		}
	}
	return rows, 5, nil
}

const sourceReplayDSL = `name: src_replay
bar: 1h
sources:
  slow: {bar: 4h}
rules:
  - id: r
    type: rank
    filter: "slow.trend > 0"
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio: {max_missing: 1}
`

// S7 研究回放：来源按 as-of 合进每一期的输入，与逐期实时装配 + 同一引擎的结果逐期一致；每段的来源读取从第一根应读的来源 bar
// 的前一根开始。场景里来源的趋势每 4 根翻转一次，A 有一期来源缺行。
func TestReplaySourcesMatchLive(t *testing.T) {
	repo := openStore(t)
	client := &sourceClient{fakeClient: &fakeClient{marketType: "spot"}}
	tasks := map[string]string{"spot": "ftask_a", "slow:spot": "ftask_s"}
	strategy, err := dsl.Parse([]byte(sourceReplayDSL))
	require.NoError(t, err)
	resolved, program, err := input.ResolveTasks(context.Background(), client, "crypto", tasks, strategy)
	require.NoError(t, err)
	job := store.Replay{ReplayID: "s1", DSLYaml: sourceReplayDSL, DSLHash: dsl.Hash([]byte(sourceReplayDSL)), Calendar: "crypto_24x7", SpaceID: "crypto",
		Tasks:     map[string]store.ReplayTask{"spot": spotTask, "slow:spot": {FactorTaskID: "ftask_s", ViewID: slowView, ViewGeneration: "idx_s"}},
		StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(22 * time.Hour), SpotFeeBps: 10, Factors: resolved.Factors, CreatedAt: origin}
	require.NoError(t, repo.CreateReplay(context.Background(), job))
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	require.NoError(t, err)
	require.True(t, found)
	(&Runner{Store: repo, Client: client, ChunkBars: 6, InitialEquity: 100}).Execute(context.Background(), claimed)
	done, err := repo.GetReplay(context.Background(), "s1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	// 第一段从 origin+3h 收盘的主帧起，应读收盘于 origin 的 4h 那根（bar_start 是 origin−4h），再向前多取一根。
	require.NotEmpty(t, client.sourceQueries)
	require.True(t, client.sourceQueries[0].Start.Equal(origin.Add(-8*time.Hour)), "第一段来源读取起点 %v", client.sourceQueries[0].Start)

	state, compared, picked := engine.State{}, 0, map[string]bool{}
	for _, replayed := range barsOf(t, repo, "s1") {
		boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, replayed.BarEndTime)
		require.NoError(t, err)
		loaded, err := (input.Loader{Client: client}).LoadBar(context.Background(), "crypto", resolved, program, input.Bar{BarStart: boundary.StorageStart, Held: state.HeldMarkets()})
		require.NoError(t, err)
		decision, err := engine.Evaluate(program, loaded.Frame, state)
		require.NoError(t, err)
		require.Equal(t, replayed.Status, decision.Status, "%v", replayed.BarEndTime)
		if decision.Status != engine.StatusOK {
			continue
		}
		_, live, err := trigger.EncodeTargets(decision.Targets)
		require.NoError(t, err)
		require.Equal(t, string(replayed.TargetsJSON), string(live), "%v 的目标不一致", replayed.BarEndTime)
		for _, target := range decision.Targets {
			picked[target.InstrumentID] = true
		}
		state = decision.State
		compared++
	}
	require.NotZero(t, compared)
	require.True(t, picked["A"] && picked["B"], "来源的趋势翻转应让 A、B 轮流入选：%v", picked)
}

// S3 参考市场的研究回放：永续单市场加现货参考市场，age_markets: all 读现货的历史判年龄（现货的 D 前 6 根没有行），
// 与逐期实时装配 + 同一引擎的结果逐期一致；参考市场不进帧、不出目标。
func TestReplayReferenceMarketMatchesLive(t *testing.T) {
	const dslYaml = `name: ref_replay
market: {select: swap, prefer: swap, reference: [spot]}
universe: {min_age_bars: 2, age_markets: all}
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 2}
    weight: 1
    side: short
portfolio: {max_missing: 1}
`
	repo := openStore(t)
	client := newBaselineClient(baselineDualClient())
	tasks := map[string]string{"swap": swapTask.FactorTaskID, "spot": spotTask.FactorTaskID}
	strategy, err := dsl.Parse([]byte(dslYaml))
	require.NoError(t, err)
	resolved, program, err := input.ResolveTasks(context.Background(), client, "crypto", tasks, strategy)
	require.NoError(t, err)
	require.Len(t, resolved.Tasks, 1)
	job := store.Replay{ReplayID: "r1", DSLYaml: dslYaml, DSLHash: dsl.Hash([]byte(dslYaml)), Calendar: "crypto_24x7", SpaceID: "crypto",
		Tasks: map[string]store.ReplayTask{"swap": swapTask, "spot": spotTask}, StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(14 * time.Hour), SwapFeeBps: 5, Factors: resolved.Factors, CreatedAt: origin}
	require.NoError(t, repo.CreateReplay(context.Background(), job))
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	require.NoError(t, err)
	require.True(t, found)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), claimed)
	done, err := repo.GetReplay(context.Background(), "r1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	state, compared, agedOut := engine.State{}, 0, false
	for _, replayed := range barsOf(t, repo, "r1") {
		boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, replayed.BarEndTime)
		require.NoError(t, err)
		loaded, err := (input.Loader{Client: client}).LoadBar(context.Background(), "crypto", resolved, program, input.Bar{BarStart: boundary.StorageStart, Held: state.HeldMarkets()})
		require.NoError(t, err)
		decision, err := engine.Evaluate(program, loaded.Frame, state)
		require.NoError(t, err)
		require.Equal(t, replayed.Status, decision.Status, "%v", replayed.BarEndTime)
		if decision.Status != engine.StatusOK {
			continue
		}
		_, live, err := trigger.EncodeTargets(decision.Targets)
		require.NoError(t, err)
		require.Equal(t, string(replayed.TargetsJSON), string(live), "%v 的目标不一致", replayed.BarEndTime)
		for _, target := range decision.Targets {
			require.Equal(t, dsl.MarketSwap, target.Market)
		}
		agedOut = agedOut || decision.Summary.Rules["r"].AgedOut > 0
		state = decision.State
		compared++
	}
	require.NotZero(t, compared)
	require.True(t, agedOut, "现货上 D 年龄不够，应出现年龄剔除")
}
