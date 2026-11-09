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
