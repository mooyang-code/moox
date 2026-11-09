package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
)

// 策略池研究回放的用例：回放逐期复用预热的推进逻辑，所以拿同一区间、同一起点的预热（RunPool）逐期比对目标与 summary_json 的池明细。

// poolMember 是一个按 mom 排名的成员：rule 是规则 r 里 score 之后的写法（每行缩进四格）。
func poolMember(t *testing.T, id, market, rule string) input.PoolMemberDef {
	t.Helper()
	raw := "name: " + id + "\nbar: 1h\nmarket: {select: " + market + "}\nrules:\n  - id: r\n    type: rank\n    score: \"mom\"\n" + rule + "    weight: {total: 1}\nportfolio:\n  max_missing: 1\n"
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return input.PoolMemberDef{ID: id, StrategyID: "s_" + id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Strategy: strategy}
}

// poolMembers 是用例共用的成员：a 现货做多第一名，c 现货做多前两名，w 永续做多第一名，s 永续做空第一名，probe 只选 mom > 1.5 的标的
// （B 每 5 根有一根是 −8，那一根为空）。
func poolMembers(t *testing.T) map[string]input.PoolMemberDef {
	return map[string]input.PoolMemberDef{
		"a":     poolMember(t, "a", "spot", "    select: {top: 1}\n"),
		"w":     poolMember(t, "w", "swap", "    select: {top: 1}\n"),
		"c":     poolMember(t, "c", "spot", "    select: {top: 2}\n"),
		"s":     poolMember(t, "s", "swap", "    select: {top: 1}\n    side: short\n"),
		"probe": poolMember(t, "probe", "spot", "    filter: \"mom > 1.5\"\n    select: {top: 1}\n    allow_empty: true\n"),
	}
}

// poolYaml 拼出一个池：body 写在 strategy_pool 之下（缩进两格），portfolio 写在顶层（可为空）。
func poolYaml(body, portfolio string, members ...input.PoolMemberDef) string {
	var list strings.Builder
	for _, member := range members {
		fmt.Fprintf(&list, "    - {id: %s, strategy: %s}\n", member.ID, member.StrategyID)
	}
	return "name: pool\nbar: 1h\nstrategy_pool:\n  members:\n" + list.String() + body + portfolio
}

// resolvePool 按发起回放时的做法解析池（成员取给定的定义）。
func resolvePool(t *testing.T, client input.Client, raw string, members []input.PoolMemberDef) (dsl.Strategy, input.PoolResolved) {
	t.Helper()
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	markets := map[string]bool{}
	for _, member := range members {
		for _, market := range member.Strategy.Market.Markets() {
			markets[market] = true
		}
	}
	tasks := []string{}
	if markets[dsl.MarketSpot] {
		tasks = append(tasks, "ftask_a")
	}
	if markets[dsl.MarketSwap] {
		tasks = append(tasks, "ftask_b")
	}
	pooled, err := input.ResolvePool(context.Background(), client, "crypto", tasks, strategy, members)
	if err != nil {
		t.Fatal(err)
	}
	return strategy, pooled
}

// startPoolReplay 登记并认领一个池回放：成员快照取 members，区间从第 2 根开始共 hours 根。
func startPoolReplay(t *testing.T, repo *store.Store, client input.Client, id, raw string, members []input.PoolMemberDef, hours int) store.Replay {
	t.Helper()
	_, pooled := resolvePool(t, client, raw, members)
	snapshot, err := pooled.MembersJSON()
	if err != nil {
		t.Fatal(err)
	}
	tasks := map[string]store.ReplayTask{}
	for market, task := range pooled.Resolved.Tasks {
		tasks[market] = store.ReplayTask{FactorTaskID: task.FactorTaskID, ViewID: task.ViewID, ViewGeneration: "idx"}
	}
	job := store.Replay{ReplayID: id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Calendar: "crypto_24x7", SpaceID: "crypto", Tasks: tasks,
		StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(time.Duration(2+hours) * time.Hour), SpotFeeBps: 10, SwapFeeBps: 5,
		MembersJSON: snapshot, Factors: pooled.Resolved.Factors, CreatedAt: origin}
	if err := repo.CreateReplay(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	if err != nil || !found || claimed.ReplayID != id {
		t.Fatalf("认领回放失败：%+v found=%v err=%v", claimed, found, err)
	}
	return claimed
}

// warmPool 用预热的历史推进（RunPool）从第 2 根推进 hours 根，返回每期的决策；配置与回放一样按池的日历换算漂移起点与锚点。
func warmPool(t *testing.T, client input.Client, raw string, members []input.PoolMemberDef, hours, chunk int) []engine.Decision {
	t.Helper()
	strategy, pooled := resolvePool(t, client, raw, members)
	clock, err := input.PoolClock(pooled.Resolved.Calendar, pooled.Resolved.Bar, *strategy.Pool)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pool.NewConfig(strategy, pooled.Resolved.Market, input.MemberStrategies(pooled.Members, pooled.Programs), clock)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []engine.Decision
	run := PoolRun{Client: client, SpaceID: "crypto", Resolved: pooled.Resolved, Members: pooled.Members, Programs: pooled.Programs, Config: cfg,
		Start: origin.Add(2 * time.Hour), End: origin.Add(time.Duration(2+hours) * time.Hour), ChunkBars: chunk,
		Now:   func() time.Time { return origin.Add(2000 * time.Hour) },
		OnBar: func(_ input.PeriodBoundaries, decision engine.Decision) { decisions = append(decisions, decision) }}
	if _, err := RunPool(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return decisions
}

// barDecision 取回放一期 summary_json 里的 decision 各字段（原始 JSON）。
func barDecision(t *testing.T, bar store.ReplayBar) map[string]json.RawMessage {
	t.Helper()
	var summary struct {
		Decision map[string]json.RawMessage `json:"decision"`
	}
	if err := json.Unmarshal(bar.SummaryJSON, &summary); err != nil {
		t.Fatal(err)
	}
	return summary.Decision
}

// requireSameAsWarmUp 逐期比对回放与预热：状态、跳过原因、目标与 summary_json 里的 pool、limits 逐字节相同。
func requireSameAsWarmUp(t *testing.T, bars []store.ReplayBar, decisions []engine.Decision) {
	t.Helper()
	if len(bars) == 0 || len(bars) != len(decisions) {
		t.Fatalf("期数不一致：回放 %d，预热 %d", len(bars), len(decisions))
	}
	for i, bar := range bars {
		want := decisions[i]
		if bar.Status != want.Status || bar.SkipReason != want.SkipReason {
			t.Fatalf("第 %d 期状态 %s/%s，预热 %s/%s", i, bar.Status, bar.SkipReason, want.Status, want.SkipReason)
		}
		targets := want.Targets
		if want.Status != engine.StatusOK {
			targets = nil
		}
		_, wantTargets, err := trigger.EncodeTargets(targets)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bar.TargetsJSON, wantTargets) {
			t.Fatalf("第 %d 期目标不一致：\n回放 %s\n预热 %s", i, bar.TargetsJSON, wantTargets)
		}
		decision := barDecision(t, bar)
		pool, _ := json.Marshal(want.Summary.Pool)
		if !bytes.Equal(decision["pool"], pool) {
			t.Fatalf("第 %d 期池明细不一致：\n回放 %s\n预热 %s", i, decision["pool"], pool)
		}
		var limits []byte
		if len(want.Summary.Limits) > 0 {
			limits, _ = json.Marshal(want.Summary.Limits)
		}
		if !bytes.Equal(decision["limits"], limits) {
			t.Fatalf("第 %d 期超限转移不一致：\n回放 %s\n预热 %s", i, decision["limits"], limits)
		}
	}
}

func replayMetrics(t *testing.T, repo *store.Store, id string) (store.Replay, Metrics) {
	t.Helper()
	done, err := repo.GetReplay(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	return done, metrics
}

// 池回放与同一区间、同一起点的预热逐期一致（轮动、择时、步长，现货与永续两个 View）；回放与预热用不同的分段，段边界上的上一根也一致。
// 回放写进度、累计指标，池目标由池级账本承接。
func TestPoolReplayMatchesWarmUpBarByBar(t *testing.T) {
	members := poolMembers(t)
	list := []input.PoolMemberDef{members["a"], members["s"], members["c"]}
	raw := poolYaml("  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 3}, {name: drawdown, window: 4}]}\n  timing: {window: 3}\n  step: 0.5\n  fees: {spot_bps: 10, swap_bps: 5}\n", "", list...)
	repo := openStore(t)
	client := newDualClient()
	job := startPoolReplay(t, repo, client, "p1", raw, list, 30)
	(&Runner{Store: repo, Client: client, ChunkBars: 7, InitialEquity: 1}).Execute(context.Background(), job)
	done, metrics := replayMetrics(t, repo, "p1")
	if done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	bars := barsOf(t, repo, "p1")
	requireSameAsWarmUp(t, bars, warmPool(t, newDualClient(), raw, list, 30, 4))
	if metrics.Bars != 30 || done.ProgressTime == nil || !done.ProgressTime.Equal(bars[len(bars)-1].BarEndTime) {
		t.Fatalf("指标与进度：bars=%d progress=%v", metrics.Bars, done.ProgressTime)
	}
	if !strings.Contains(strings.Join(metrics.Limitations, "；"), "策略池") {
		t.Fatalf("局限说明应写明池回放的口径：%v", metrics.Limitations)
	}
	// 池级账本承接合成后的池目标：有目标的期权益随价格变化。
	moved := false
	for _, bar := range bars {
		moved = moved || bar.Equity != 1
	}
	if !moved || metrics.AverageHoldings == 0 {
		t.Fatalf("池级账本应按池目标持仓：%+v", metrics)
	}
}

// drift、超限转移（P1）、条件转移（P2）、净敞口（P4）、总杠杆（P5）、波动冻结（P6）各一个回放用例：都与预热逐期一致，且功能在回放里真的生效。
func TestPoolReplayFeaturesMatchWarmUp(t *testing.T) {
	members := poolMembers(t)
	a, c, s, probe := members["a"], members["c"], members["s"], members["probe"]
	start := origin.Add(2 * time.Hour).Format(time.RFC3339)
	anyPool := func(decisions []engine.Decision, check func(engine.Decision) bool) bool {
		for _, decision := range decisions {
			if decision.Summary.Pool != nil && check(decision) {
				return true
			}
		}
		return false
	}
	hasAdjustment := func(kind string) func(engine.Decision) bool {
		return func(decision engine.Decision) bool {
			for _, item := range decision.Summary.Pool.Adjustments {
				if item.Kind == kind {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name      string
		body      string
		portfolio string
		members   []input.PoolMemberDef
		effective func(engine.Decision) bool
	}{
		{
			name:    "drift",
			body:    "  allocation:\n    method: drift\n    ratios: {a: 0.3, c: 0.3, s: 0.4}\n    start: \"" + start + "\"\n    sides: {long: {rebalance: periodic, period_bars: 3, spread_bars: 2}}\n",
			members: []input.PoolMemberDef{a, c, s},
			effective: func(decision engine.Decision) bool {
				for _, side := range decision.Summary.Pool.Drift.Sides {
					if side.Trigger == "periodic" && side.Moved {
						return true
					}
				}
				return false
			},
		},
		{
			name:      "limits",
			body:      "  allocation: {method: fixed, ratios: {a: 0.6, c: 0.4}}\n",
			portfolio: "portfolio:\n  limits: {long_max: 0.5, overflow: {to: c}}\n",
			members:   []input.PoolMemberDef{a, c},
			effective: func(decision engine.Decision) bool { return len(decision.Summary.Limits) > 0 },
		},
		{
			name:      "switch",
			body:      "  allocation:\n    method: fixed\n    ratios: {a: 0.5, c: 0.5, probe: 0}\n    switch:\n      - when: {empty: probe}\n        move: {from: c, to: [a]}\n",
			members:   []input.PoolMemberDef{a, c, probe},
			effective: func(decision engine.Decision) bool { return decision.Summary.Pool.Switch != nil },
		},
		{
			name:      "exposure",
			body:      "  allocation: {method: fixed, ratios: {a: 0.8, s: 0.2}}\n  exposure:\n    bands:\n      - {min: -0.1, max: 0.3, adjust: reduce_larger}\n",
			members:   []input.PoolMemberDef{a, s},
			effective: hasAdjustment("exposure"),
		},
		{
			name:      "gross",
			body:      "  allocation: {method: fixed, ratios: {a: 0.8, w: 0.8}}\n  gross: {min: 1.0, max: 1.2}\n",
			portfolio: "portfolio: {leverage: 2}\n",
			members:   []input.PoolMemberDef{a, members["w"]},
			effective: hasAdjustment("gross"),
		},
		{
			name: "freeze",
			body: "  allocation:\n    method: drift\n    ratios: {a: 0.4, c: 0.3, s: 0.3}\n    sides: {long: {rebalance: every_bar}}\n" +
				"  freeze: {watch: long, window_bars: 1, threshold: 0.002, bars: 2}\n",
			members: []input.PoolMemberDef{a, c, s},
			effective: func(decision engine.Decision) bool {
				return decision.Summary.Pool.Drift != nil && decision.Summary.Pool.Drift.Freeze != nil && decision.Summary.Pool.Drift.Freeze.Triggered
			},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			raw := poolYaml(item.body+"  fees: {spot_bps: 10, swap_bps: 5}\n", item.portfolio, item.members...)
			repo := openStore(t)
			client := newDualClient()
			job := startPoolReplay(t, repo, client, "p_"+item.name, raw, item.members, 24)
			(&Runner{Store: repo, Client: client, ChunkBars: 5, InitialEquity: 1}).Execute(context.Background(), job)
			if done, _ := replayMetrics(t, repo, job.ReplayID); done.Status != store.ReplayDone {
				t.Fatalf("回放应完成：%+v", done)
			}
			decisions := warmPool(t, newDualClient(), raw, item.members, 24, 4)
			requireSameAsWarmUp(t, barsOf(t, repo, job.ReplayID), decisions)
			if !anyPool(decisions, item.effective) {
				t.Fatalf("%s 在回放区间里没有生效，用例没有检验到它", item.name)
			}
		})
	}
}

// 排队期间改了成员定义：执行时只读回放记录里的成员快照，结果与按旧版本回放相同，与按新版本回放不同。
func TestPoolReplayIgnoresMemberEditsWhileQueued(t *testing.T) {
	members := poolMembers(t)
	v1 := []input.PoolMemberDef{members["a"], members["c"]}
	raw := poolYaml("  allocation: {method: fixed, ratios: {a: 0.5, c: 0.5}}\n", "", v1...)
	edited := poolMember(t, "a", "spot", "    select: {top: 3}\n")
	v2 := []input.PoolMemberDef{edited, members["c"]}

	run := func(id string, snapshot []input.PoolMemberDef, beforeRun func(repo *store.Store)) []store.ReplayBar {
		repo := openStore(t)
		client := &fakeClient{marketType: "spot"}
		job := startPoolReplay(t, repo, client, id, raw, snapshot, 12)
		if beforeRun != nil {
			beforeRun(repo)
		}
		(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 1}).Execute(context.Background(), job)
		if done, _ := replayMetrics(t, repo, id); done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v", done)
		}
		return barsOf(t, repo, id)
	}
	// 排队之后成员 a 的定义改成 v2（库里只有新版本）。
	queued := run("queued", v1, func(repo *store.Store) {
		if err := repo.CreateDefinition(context.Background(), store.Definition{StrategyID: edited.StrategyID, Name: "a", DSLYaml: edited.DSLYaml, DSLHash: edited.DSLHash, CreatedAt: origin}); err != nil {
			t.Fatal(err)
		}
	})
	old := run("old", v1, nil)
	changed := run("changed", v2, nil)
	same, differs := len(queued) == len(old), false
	for i := range queued {
		same = same && bytes.Equal(queued[i].TargetsJSON, old[i].TargetsJSON) && bytes.Equal(queued[i].SummaryJSON, old[i].SummaryJSON)
		differs = differs || !bytes.Equal(queued[i].TargetsJSON, changed[i].TargetsJSON)
	}
	if !same {
		t.Fatal("排队期间改成员定义，回放结果应与旧版本相同")
	}
	if !differs {
		t.Fatal("新旧版本的成员结果应不同，否则用例检验不到快照")
	}
}

// 回放过程中一个 View 换了索引：两个成员读同一个 View，整个池只跟随一次、只记一次换代，结果与没有换代时相同。
func TestPoolReplayFollowsIndexSwitchOnce(t *testing.T) {
	members := poolMembers(t)
	list := []input.PoolMemberDef{members["a"], members["c"]}
	raw := poolYaml("  allocation: {method: fixed, ratios: {a: 0.5, c: 0.5}}\n", "", list...)
	execute := func(id string, client *fakeClient) ([]store.ReplayBar, Metrics) {
		repo := openStore(t)
		job := startPoolReplay(t, repo, client, id, raw, list, 12)
		(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 1}).Execute(context.Background(), job)
		done, metrics := replayMetrics(t, repo, id)
		if done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v", done)
		}
		return barsOf(t, repo, id), metrics
	}
	switched := &fakeClient{marketType: "spot", switchAtQuery: 3, switchedFrom: origin}
	bars, metrics := execute("switched", switched)
	if switched.activeIndex != "idx2" {
		t.Fatal("用例应在回放过程中换代")
	}
	notes := strings.Join(metrics.Limitations, "；")
	if !strings.Contains(notes, "重建了 1 次索引") {
		t.Fatalf("两个成员读同一个 View，只应记一次换代：%s", notes)
	}
	plain, _ := execute("plain", &fakeClient{marketType: "spot"})
	if len(plain) != len(bars) {
		t.Fatalf("期数不一致：%d %d", len(plain), len(bars))
	}
	for i := range bars {
		if !bytes.Equal(bars[i].TargetsJSON, plain[i].TargetsJSON) || !bytes.Equal(bars[i].SummaryJSON, plain[i].SummaryJSON) {
			t.Fatalf("第 %d 期换代前后结果应相同", i)
		}
	}
}

// 资金费：池级账本与成员账本都按池 DSL 的 fees.funding_column 结算（回放任务上不写资金费列）。
func TestPoolReplaySettlesFundingFromPoolFees(t *testing.T) {
	members := poolMembers(t)
	list := []input.PoolMemberDef{members["s"]}
	execute := func(id, fees string) ([]store.ReplayBar, Metrics) {
		client := newDualClient()
		client.swap.fundingRate = 0.01
		raw := poolYaml("  allocation: {method: fixed, ratios: {s: 1}}\n"+fees, "", list...)
		repo := openStore(t)
		job := startPoolReplay(t, repo, client, id, raw, list, 12)
		(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 1}).Execute(context.Background(), job)
		done, metrics := replayMetrics(t, repo, id)
		if done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v", done)
		}
		return barsOf(t, repo, id), metrics
	}
	funded, metrics := execute("funded", "  fees: {funding_column: funding_rate}\n")
	plain, plainMetrics := execute("plain", "")
	if !strings.Contains(strings.Join(metrics.Limitations, "；"), "资金费按 funding_rate 列结算") || strings.Contains(strings.Join(plainMetrics.Limitations, "；"), "funding_rate") {
		t.Fatalf("局限说明按池 DSL 的资金费列写：%v / %v", metrics.Limitations, plainMetrics.Limitations)
	}
	last := len(funded) - 1
	// 空头在费率为正时收资金费：池级账本的权益高于不计资金费的。
	if funded[last].Equity <= plain[last].Equity {
		t.Fatalf("池级账本应结算资金费：%v <= %v", funded[last].Equity, plain[last].Equity)
	}
}

// 非法的池回放记录：没有成员快照、快照里的成员与池对不上，执行时失败并说明原因。
func TestPoolReplayRejectsBrokenSnapshot(t *testing.T) {
	members := poolMembers(t)
	list := []input.PoolMemberDef{members["a"], members["c"]}
	raw := poolYaml("  allocation: {method: fixed, ratios: {a: 0.5, c: 0.5}}\n", "", list...)
	for name, edit := range map[string]func(job *store.Replay){
		"empty":    func(job *store.Replay) { job.MembersJSON = "" },
		"mismatch": func(job *store.Replay) { job.MembersJSON = strings.Replace(job.MembersJSON, `"id":"c"`, `"id":"x"`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			repo := openStore(t)
			client := &fakeClient{marketType: "spot"}
			_, pooled := resolvePool(t, client, raw, list)
			snapshot, _ := pooled.MembersJSON()
			job := store.Replay{ReplayID: "broken", DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Calendar: "crypto_24x7", SpaceID: "crypto",
				Tasks:     map[string]store.ReplayTask{"spot": {FactorTaskID: "ftask_a", ViewID: "view_factor_a", ViewGeneration: "idx"}},
				StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(14 * time.Hour), MembersJSON: snapshot, Factors: pooled.Resolved.Factors, CreatedAt: origin}
			edit(&job)
			if err := repo.CreateReplay(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			claimed, _, err := repo.ClaimNextReplay(context.Background(), origin)
			if err != nil {
				t.Fatal(err)
			}
			(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), claimed)
			done, _ := repo.GetReplay(context.Background(), "broken")
			if done.Status != store.ReplayFailed || !strings.Contains(done.Error, "成员") {
				t.Fatalf("应以成员快照的问题失败：%s %q", done.Status, done.Error)
			}
		})
	}
}

// 回放记录的成员快照必须是非空的 JSON 数组。
func TestCreateReplayRejectsInvalidMembersSnapshot(t *testing.T) {
	repo := openStore(t)
	for _, raw := range []string{"[]", "{}", "not json"} {
		job := store.Replay{ReplayID: "bad", DSLYaml: replayDSL, DSLHash: dsl.Hash([]byte(replayDSL)), Calendar: "crypto_24x7", SpaceID: "crypto",
			Tasks:     map[string]store.ReplayTask{"spot": {FactorTaskID: "ftask_a", ViewID: "view_factor_a", ViewGeneration: "idx"}},
			StartTime: origin, EndTime: origin.Add(time.Hour), MembersJSON: raw, CreatedAt: origin}
		if err := repo.CreateReplay(context.Background(), job); err == nil || !strings.Contains(err.Error(), "成员快照") {
			t.Fatalf("成员快照 %q 应被拒绝：%v", raw, err)
		}
	}
}
