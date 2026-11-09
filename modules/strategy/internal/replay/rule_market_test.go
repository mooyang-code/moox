package replay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/stretchr/testify/require"
)

// 规则级市场（S4）的双市场回放：现货规则、永续规则与按币选行的规则同时存在，永续第 7 小时 D 缺一行；研究回放与逐期实时装配 + 同一引擎的
// 目标、各规则的年龄剔除数与缺数逐期一致，并且永续规则的目标确实落在永续、现货规则的落在现货。
func TestReplayRuleMarketMatchesLive(t *testing.T) {
	const dslYaml = `name: rule_market
market: {select: both, prefer: spot}
universe: {min_age_bars: 3}
rules:
  - id: perp
    type: rank
    market: swap
    score: "mom"
    select: {top: 1}
    weight: 0.4
  - id: cash
    type: rank
    market: spot
    score: "mom"
    select: {top: 1}
    weight: 0.3
  - id: any
    type: rank
    side: short
    score: "-mom"
    select: {top: 1}
    weight: 0.3
portfolio:
  leverage: 2
  max_missing: 1
`
	dual := baselineDualClient()
	dual.swap.missing["D"] = map[int]bool{7: true}
	client := newBaselineClient(dual)
	repo := openStore(t)
	tasks := map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}
	job := startMarketReplay(t, repo, client, "m1", dslYaml, 16, tasks, 10, 5)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "m1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	strategy, err := dsl.Parse([]byte(dslYaml))
	require.NoError(t, err)
	resolved, program, err := input.Resolve(context.Background(), client, "crypto", input.TaskIDs(map[string]string{"spot": spotTask.FactorTaskID, "swap": swapTask.FactorTaskID}), strategy)
	require.NoError(t, err)
	require.Equal(t, dsl.MarketSwap, resolved.RuleAges["perp"].Market)
	state, compared := engine.State{}, 0
	sawMarket := map[string]bool{}
	for _, replayed := range barsOf(t, repo, "m1") {
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
		for _, id := range []string{"perp", "cash", "any"} {
			live, replay := decision.Summary.Rules[id], summary.Decision.Rules[id]
			require.Equal(t, live.Expected, replay.Expected, "%v 规则 %s 的预期数不一致", replayed.BarEndTime, id)
			require.Equal(t, live.AgedOut, replay.AgedOut, "%v 规则 %s 的年龄剔除数不一致", replayed.BarEndTime, id)
			require.Equal(t, live.Missing, replay.Missing, "%v 规则 %s 的缺数不一致", replayed.BarEndTime, id)
		}
		if decision.Status != engine.StatusOK {
			continue
		}
		for _, item := range decision.Items {
			if item.Stage != engine.StageWeighted || strings.HasPrefix(item.Weight, "-") {
				continue
			}
			want := map[string]string{"perp": dsl.MarketSwap, "cash": dsl.MarketSpot}[item.RuleID]
			if want != "" {
				require.Equal(t, want, item.Market, "%v 规则 %s 的 %s 落在了错误的市场", replayed.BarEndTime, item.RuleID, item.InstrumentID)
				sawMarket[item.RuleID] = true
			}
		}
		_, live, err := trigger.EncodeTargets(decision.Targets)
		require.NoError(t, err)
		require.Equal(t, string(replayed.TargetsJSON), string(live), "%v 的目标不一致", replayed.BarEndTime)
		state = decision.State
		compared++
	}
	require.NotZero(t, compared)
	require.True(t, sawMarket["perp"] && sawMarket["cash"], "两条规则级市场的规则都应有入选：%v", sawMarket)
}
