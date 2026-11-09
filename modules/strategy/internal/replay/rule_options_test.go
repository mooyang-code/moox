package replay

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/stretchr/testify/require"
)

// 第 3 期的有状态写法（S2 进出场持有、S5 日历换仓与锚点、S6 仓位系数）在研究回放里与逐期实时装配 + 同一引擎的结果逐字节一致：
// 实时一侧逐期把上一期的状态传给下一期。回放把已停用的标的也当作活跃（见 loadMarketViews），实时不会，所以只选 1 个、事件名单与现有用例相同。
func TestReplayRuleOptionsMatchLive(t *testing.T) {
	cases := map[string]string{
		"hold": `name: hold
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    hold: {exit: "mom < 0", max_bars: 3, cooldown_bars: 1}
    weight: 1
portfolio: {max_missing: 1}
`,
		"calendar": `name: calendar
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    holding: {calendar: trading_days, every: 1, days: [1], at: first}
    weight: 1
portfolio: {max_missing: 1}
`,
		"anchor": `name: anchor
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    holding: {bars: 3, offsets: [0, 1], anchor: "2026-01-01T01:00:00Z"}
    weight: 1
portfolio: {max_missing: 1}
`,
		"on_missing": `name: on_missing
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    on_missing: {keep_bars: 2}
    weight: 1
portfolio: {max_missing: 1}
`,
		"on_missing_row": `name: on_missing_row
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    on_missing: {keep_bars: 2}
    weight: 1
portfolio: {max_missing: 1}
`,
		"hold_on_missing_row": `name: hold_on_missing_row
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    hold: {}
    on_missing: {keep_bars: 2}
    weight: 1
portfolio: {max_missing: 1}
`,
		"scale": `name: scale
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    scale: {expr: "close / 200", max: 1}
    weight: 1
portfolio: {max_missing: 1}
`,
	}
	for name, dslYaml := range cases {
		t.Run(name, func(t *testing.T) {
			repo := openStore(t)
			client := &fakeClient{marketType: "spot"}
			switch name {
			case "on_missing":
				// 两个标的轮流有两根缺 mom（行还在）：持有中的那个应按 on_missing 冻结保持。
				client.nullMom = map[string]map[int]bool{"A": {6: true, 7: true}, "B": {9: true, 10: true}}
			case "on_missing_row", "hold_on_missing_row":
				// 两个标的轮流有两根整行缺失：回放与实时一样把持有中的那个留在预期集合里，按 on_missing 冻结保持。
				client.missing = map[string]map[int]bool{"A": {6: true, 7: true}, "B": {9: true, 10: true}}
			}
			job := startReplay(t, repo, "p1", dslYaml, 12)
			(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), job)
			done, err := repo.GetReplay(context.Background(), "p1")
			require.NoError(t, err)
			require.Equal(t, store.ReplayDone, done.Status, done.Error)
			strategy, err := dsl.Parse([]byte(dslYaml))
			require.NoError(t, err)
			resolved, program, err := input.Resolve(context.Background(), client, "crypto", []string{"ftask_a"}, strategy)
			require.NoError(t, err)
			state, compared, kept := engine.State{}, 0, false
			for _, replayed := range barsOf(t, repo, "p1") {
				boundary, err := input.FromBarEnd(resolved.Calendar, resolved.Bar, replayed.BarEndTime)
				require.NoError(t, err)
				loaded, err := (input.Loader{Client: client}).LoadBar(context.Background(), "crypto", resolved, program, input.Bar{BarStart: boundary.StorageStart, EventUniverse: map[string][]string{resolved.Market.Select: {"A", "B"}}})
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
				state = decision.State
				compared++
				for _, item := range decision.Items {
					kept = kept || item.Reason == "missing_kept"
				}
			}
			require.NotZero(t, compared)
			if name == "on_missing" || name == "on_missing_row" || name == "hold_on_missing_row" {
				require.True(t, kept, "应出现按 on_missing 冻结保持的持仓")
			}
		})
	}
}
