package replay

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/stretchr/testify/require"
)

// 双市场、永续某几根缺行（A 的永续第 9、10 小时没有行，B 的永续第 8 小时才上线）：age_markets: all 规则与 each 的 max_age_bars 规则，
// 研究回放与逐期实时装配 + 同一引擎的结果逐字节一致（两边“本期列出的标的”都取市场的全部标的，本根没有行也算列出）。
func TestReplayRuleAgesMatchLiveWithMissingRows(t *testing.T) {
	const dslYaml = `name: ages
market: {select: both, prefer: swap}
rules:
  - id: fresh
    type: rank
    score: "mom"
    select: {top: 1}
    weight: 0.5
    allow_empty: true
    universe: {max_age_bars: 3}
  - id: listed
    type: rank
    score: "mom"
    select: {top: 1}
    weight: 0.5
    allow_empty: true
    universe: {min_age_bars: 2, age_markets: all}
portfolio: {max_missing: 1}
`
	repo := openStore(t)
	client := newBaselineClient(baselineDualClient())
	tasks := map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}
	job := startMarketReplay(t, repo, client, "a1", dslYaml, 16, tasks, 10, 5)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "a1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	strategy, err := dsl.Parse([]byte(dslYaml))
	require.NoError(t, err)
	resolved, program, err := input.Resolve(context.Background(), client, "crypto", input.TaskIDs(map[string]string{"spot": spotTask.FactorTaskID, "swap": swapTask.FactorTaskID}), strategy)
	require.NoError(t, err)
	state, compared, aged := engine.State{}, 0, false
	for _, replayed := range barsOf(t, repo, "a1") {
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
		// 各规则的集合划分（预期、年龄剔除、可用、缺数）也要一致：年龄口径不同时目标未必变，但年龄剔除数会变。
		var summary struct {
			Decision engine.Summary `json:"decision"`
		}
		require.NoError(t, json.Unmarshal(replayed.SummaryJSON, &summary))
		for _, id := range []string{"fresh", "listed"} {
			require.Equal(t, decision.Summary.Rules[id].AgedOut, summary.Decision.Rules[id].AgedOut, "%v 规则 %s 的年龄剔除数不一致", replayed.BarEndTime, id)
		}
		aged = aged || decision.Summary.Rules["fresh"].AgedOut > 0 || decision.Summary.Rules["listed"].AgedOut > 0
		state = decision.State
		compared++
	}
	require.NotZero(t, compared)
	require.True(t, aged, "场景里应出现规则级年龄剔除")
}

// 只写策略级 min_age_bars 的双市场回放（计划 0.2 的例外：对齐实时的缺陷修复）：D 的现货是新币、永续是老币，永续第 7 小时缺一行（现货第 6 小时才上线，这时还不满 3 根）。
// 年龄探针的候选取市场的全部标的（与实时一致），D 在永续上照常判为年龄足够，这一根按缺数处理；旧回放只探针本根有行的标的，
// 会把它记成年龄剔除，与实时不一致。逐期与实时对拍目标、状态以及各规则的年龄剔除数、缺数。
func TestReplayStrategyAgeMatchesLiveWhenSwapRowMissing(t *testing.T) {
	const dslYaml = `name: dual_age
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
	dual := baselineDualClient()
	dual.swap.missing["D"] = map[int]bool{7: true}
	client := newBaselineClient(dual)
	repo := openStore(t)
	tasks := map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}
	job := startMarketReplay(t, repo, client, "d1", dslYaml, 16, tasks, 10, 5)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "d1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	strategy, err := dsl.Parse([]byte(dslYaml))
	require.NoError(t, err)
	resolved, program, err := input.Resolve(context.Background(), client, "crypto", input.TaskIDs(map[string]string{"spot": spotTask.FactorTaskID, "swap": swapTask.FactorTaskID}), strategy)
	require.NoError(t, err)
	require.Nil(t, resolved.RuleAges, "这个场景只有策略级 min_age_bars")
	state, compared, sawMissingD := engine.State{}, 0, false
	for _, replayed := range barsOf(t, repo, "d1") {
		boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, replayed.BarEndTime)
		require.NoError(t, err)
		loaded, err := (input.Loader{Client: client}).LoadBar(context.Background(), "crypto", resolved, program, input.Bar{BarStart: boundary.StorageStart, Held: state.HeldMarkets()})
		require.NoError(t, err)
		decision, err := engine.Evaluate(program, loaded.Frame, state)
		require.NoError(t, err)
		require.Equal(t, replayed.Status, decision.Status, "%v", replayed.BarEndTime)
		var summary struct {
			Decision engine.Summary `json:"decision"`
		}
		require.NoError(t, json.Unmarshal(replayed.SummaryJSON, &summary))
		for _, id := range []string{"l", "s"} {
			live, replay := decision.Summary.Rules[id], summary.Decision.Rules[id]
			require.Equal(t, live.AgedOut, replay.AgedOut, "%v 规则 %s 的年龄剔除数不一致", replayed.BarEndTime, id)
			require.Equal(t, live.Missing, replay.Missing, "%v 规则 %s 的缺数不一致", replayed.BarEndTime, id)
		}
		if hourOf(replayed) == 7 {
			for _, item := range decision.Items {
				sawMissingD = sawMissingD || (item.InstrumentID == "D" && item.Stage == engine.StageMissing)
			}
		}
		if decision.Status != engine.StatusOK {
			continue
		}
		_, live, err := trigger.EncodeTargets(decision.Targets)
		require.NoError(t, err)
		require.Equal(t, string(replayed.TargetsJSON), string(live), "%v 的目标不一致", replayed.BarEndTime)
		state = decision.State
		compared++
	}
	require.NotZero(t, compared)
	require.True(t, sawMissingD, "第 7 小时永续缺行的 D 应按缺数处理（年龄按永续判为足够）")
}

// 双市场策略，永续单边断档（第 9、10 小时永续整根没有行，现货照常），max_missing 用默认值。真实实时路径上断档时事件名单仍是
// 本期预期全集、全部标为失败，这个市场被判为整体不可用（这里用 Unavailable 表达）；回放在一个市场整根无行、另一个市场有行时按同样的
// 口径给它构造 market_unavailable 证据。两边走同一条路径：持仓按 market_hold 冻结（只在永续上市、被空头规则持有的 F 也保留），
// 空头规则守门豁免。写不写 on_missing 都逐期对拍状态、目标与摘要（各规则的全部计数与说明），断档期间也比。
func TestReplayOneMarketGapMatchesLive(t *testing.T) {
	for _, onMissing := range []string{"", "\n    on_missing: {keep_bars: 3}"} {
		dslYaml := `name: one_gap
market: {select: both, prefer: swap}
universe:
  exclude: [E]
rules:
  - id: l
    type: rank
    score: "mom"
    select: {top: 1}
    weight: 0.6` + onMissing + `
  - id: s
    type: rank
    side: short
    score: "-mom"
    select: {top: 1}
    weight: 0.4` + onMissing + `
portfolio:
  leverage: 2
`
		gap := map[int]bool{9: true, 10: true}
		dual := baselineDualClient()
		swapIDs := append(append([]string(nil), baselineSubjects...), "F")
		for _, id := range swapIDs {
			if dual.swap.missing[id] == nil {
				dual.swap.missing[id] = map[int]bool{}
			}
			for hour := range gap {
				dual.swap.missing[id][hour] = true
			}
		}
		// F 只在永续上市（动量多数时候最低，是空头规则的持仓）。
		client := &swapOnlyClient{baselineClient: newBaselineClient(dual), swapDataset: "ds_swap", extra: "F"}
		repo := openStore(t)
		tasks := map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}
		job := startMarketReplay(t, repo, client, "o1", dslYaml, 16, tasks, 10, 5)
		(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
		done, err := repo.GetReplay(context.Background(), "o1")
		require.NoError(t, err)
		require.Equal(t, store.ReplayDone, done.Status, done.Error)
		strategy, err := dsl.Parse([]byte(dslYaml))
		require.NoError(t, err)
		resolved, program, err := input.Resolve(context.Background(), client, "crypto", input.TaskIDs(map[string]string{"spot": spotTask.FactorTaskID, "swap": swapTask.FactorTaskID}), strategy)
		require.NoError(t, err)
		// 实时的事件名单：每个市场本根有行的标的；断档的那两根，永续的名单仍是全集，并整体不可用。
		names := func(ids []string, missing map[string]map[int]bool, hour int) []string {
			out := []string{}
			for _, id := range ids {
				if !missing[id][hour] || gap[hour] {
					out = append(out, id)
				}
			}
			return out
		}
		heldF := func(state engine.State) bool {
			_, ok := state.HeldMarkets()[dsl.MarketSwap]["F"]
			return ok
		}
		state, compared, gapBars, keptF := engine.State{}, 0, 0, false
		for _, replayed := range barsOf(t, repo, "o1") {
			hour := hourOf(replayed)
			boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, replayed.BarEndTime)
			require.NoError(t, err)
			bar := input.Bar{BarStart: boundary.StorageStart, Held: state.HeldMarkets(), EventUniverse: map[string][]string{
				dsl.MarketSpot: names(baselineSubjects, dual.spot.missing, hour), dsl.MarketSwap: names(swapIDs, dual.swap.missing, hour),
			}}
			if gap[hour] {
				bar.Unavailable = map[string]string{dsl.MarketSwap: "永续本期整体失败"}
			}
			loaded, err := (input.Loader{Client: client}).LoadBar(context.Background(), "crypto", resolved, program, bar)
			require.NoError(t, err)
			decision, err := engine.Evaluate(program, loaded.Frame, state)
			require.NoError(t, err)
			require.Equal(t, replayed.Status, decision.Status, "第 %d 小时（%q）", hour, onMissing)
			require.Equal(t, replayed.SkipReason, decision.SkipReason, "第 %d 小时", hour)
			var summary struct {
				Decision engine.Summary `json:"decision"`
			}
			require.NoError(t, json.Unmarshal(replayed.SummaryJSON, &summary))
			require.Equal(t, decision.Summary.Rules, summary.Decision.Rules, "第 %d 小时各规则的摘要不一致（%q）", hour, onMissing)
			require.Equal(t, decision.Summary.Notes, summary.Decision.Notes, "第 %d 小时的说明不一致", hour)
			if gap[hour] {
				gapBars++
				if heldF(state) {
					require.True(t, targetsHold(t, replayed, "F"), "第 %d 小时 F 应保留", hour)
					keptF = true
				}
			}
			if decision.Status != engine.StatusOK {
				continue
			}
			_, live, err := trigger.EncodeTargets(decision.Targets)
			require.NoError(t, err)
			require.Equal(t, string(replayed.TargetsJSON), string(live), "第 %d 小时的目标不一致（%q）", hour, onMissing)
			state = decision.State
			compared++
		}
		require.NotZero(t, compared)
		require.Equal(t, 2, gapBars)
		require.True(t, keptF, "断档期间空头规则持有的 F 应保留")
	}
}

// targetsHold 报告回放这一期的目标里标的是否仍有永续持仓（含保持目标）。
func targetsHold(t *testing.T, bar store.ReplayBar, id string) bool {
	t.Helper()
	_, ok := decodeTargets(t, bar)[id+"|swap"]
	return ok
}

// swapOnlyClient 在永续数据集的标的绑定里多一个只在永续上市的标的。
type swapOnlyClient struct {
	*baselineClient
	swapDataset string
	extra       string
}

func (c *swapOnlyClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]input.Subject, error) {
	subjects, err := c.baselineClient.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err == nil && datasetID == c.swapDataset {
		subjects = append(subjects, input.Subject{SubjectID: c.extra, Active: true})
	}
	return subjects, err
}
