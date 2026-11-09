package warmup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// driftYaml 是两个成员的 drift 池，allocation 写在 method、ratios 之后；freeze 的窗口让预热期数大于 1。
func driftYaml(allocation string) string {
	return `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: s_a}
    - {id: b, strategy: s_b}
  allocation:
    method: drift
    ratios: {a: 0.6, b: 0.4}
` + allocation + `    sides: {long: {rebalance: periodic, period_bars: 6}}
  freeze: {watch: long, window_bars: 5, threshold: 0.5, bars: 2}
`
}

func hourText(hours int) string {
	return origin.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339)
}

func barIndexAt(t *testing.T, hours int) int64 {
	t.Helper()
	bar, err := input.FromStorageStart(input.DefaultCalendar, "1h", origin.Add(time.Duration(hours)*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return bar.BarIndex
}

// warmedDrift 返回会话预热结果里池的 drift 状态。
func warmedDrift(t *testing.T, session store.Session) *pool.DriftState {
	t.Helper()
	var state engine.State
	if err := json.Unmarshal([]byte(session.WarmState), &state); err != nil {
		t.Fatal(err)
	}
	decoded, err := pool.DecodeState(state.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Drift
}

// 写了 allocation.start：预热从它开始推进到最新已收盘的一根，漂移起点就是它；停用再启用（新会话）从 start 重新预热，结果与第一次相同。
func TestDriftWarmUpStartsAtAllocationStart(t *testing.T) {
	yaml := driftYaml("    start: \"" + hourText(80) + "\"\n")
	h := newPoolHarness(t, yaml, "a", "b")
	session := h.session(t)
	resolved, _ := input.ParseResolved([]byte(session.ResolvedJSON))
	_, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dsl.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := Window(resolved, strategy, programs, h.manager.Now(), time.Minute)
	if err != nil || !start.Equal(origin.Add(80*time.Hour)) {
		t.Fatalf("预热起点 %v，期望 allocation.start；err=%v", start, err)
	}
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "预热完成", func() bool { return h.session(t).WarmedThrough != nil })
	first := h.session(t)
	if !first.WarmedThrough.Equal(origin.Add(100 * time.Hour)) {
		t.Fatalf("预热终点 %v", first.WarmedThrough)
	}
	if drift := warmedDrift(t, first); drift == nil || drift.Start != barIndexAt(t, 80) {
		t.Fatalf("漂移起点应是 allocation.start 那一根：%+v", drift)
	}

	// 停用再启用：旧会话关闭，新会话从 start 重新预热，不带旧会话的任何状态。
	ctx := context.Background()
	previous := "session-1"
	if err := h.repo.DisableInstance(ctx, "i1", &previous, nil, "", origin); err != nil {
		t.Fatal(err)
	}
	next := "session-2"
	if err := h.repo.OpenSession(ctx, store.Session{SessionID: next, InstanceID: "i1", DSLHash: first.DSLHash, ResolvedJSON: first.ResolvedJSON, MembersJSON: first.MembersJSON, CreatedAt: origin}, yaml); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		if err := h.repo.SetInstanceEnabled(ctx, "i1", enabled, &next, json.RawMessage(first.ResolvedJSON), origin); err != nil {
			t.Fatal(err)
		}
	}
	h.manager.Start(ctx, "i1", next)
	var second store.Session
	eventually(t, "重新预热完成", func() bool {
		second, err = h.repo.GetSession(ctx, next)
		return err == nil && second.WarmedThrough != nil
	})
	if second.WarmState != first.WarmState {
		t.Fatalf("重新启用后应从 start 重新预热、结果与第一次相同：\n第一次 %s\n第二次 %s", first.WarmState, second.WarmState)
	}
}

// drift 池预热之后的第一期走实时路径（按会话换算漂移起点与锚点）：与从 start 一路推进到这一期逐位一致。
// 锚点在第 82 小时、每 6 根触发：第一期（第 100 小时）是触发点；按默认锚点（起点第 80 小时）则不是，实时路径漏掉换算就对不上。
func TestDriftFirstLivePeriodMatchesOneLongRun(t *testing.T) {
	yaml := strings.Replace(driftYaml("    start: \""+hourText(80)+"\"\n"), "period_bars: 6}", "period_bars: 6, anchor: \""+hourText(82)+"\"}", 1)
	decision := checkFirstLiveMatchesLongRun(t, yaml, "a", "b")
	if sides := decision.Summary.Pool.Drift.Sides; len(sides) != 1 || sides[0].Trigger != "periodic" {
		t.Fatalf("第一期应按锚点触发 periodic：%+v", sides)
	}
}

// 写了 allocation.initial：预热从 at 往前推成员所需的预热期数开始，之前的根还没到漂移起点；比例落在 at 那一根上，之后照常漂移。
func TestDriftWarmUpInitialRatiosLandOnAt(t *testing.T) {
	yaml := driftYaml("    initial: {at: \"" + hourText(90) + "\", ratios: {a: 0.7, b: 0.3}}\n")
	h := newPoolHarness(t, yaml, "a", "b")
	session := h.session(t)
	resolved, _ := input.ParseResolved([]byte(session.ResolvedJSON))
	members, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dsl.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	// 预热期数 = 冻结窗口 5 + 1 = 6：从第 85 小时起，第 90 小时是其中最后一期。
	start, end, err := Window(resolved, strategy, programs, h.manager.Now(), time.Minute)
	if err != nil || !start.Equal(origin.Add(85*time.Hour)) {
		t.Fatalf("预热起点 %v，期望 at 往前 6 期；err=%v", start, err)
	}
	clock, err := input.PoolClock(resolved.Calendar, resolved.Bar, *strategy.Pool)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pool.NewConfig(strategy, resolved.Market, input.MemberStrategies(members, programs), clock)
	if err != nil {
		t.Fatal(err)
	}
	phases := map[int]string{}
	ratios := map[int]map[string]string{}
	run := replay.PoolRun{Client: h.client, SpaceID: "crypto", Resolved: resolved, Members: members, Programs: programs, Config: cfg,
		Start: start, End: end, IncludeLatest: true, ChunkBars: 4, Now: h.manager.Now,
		OnBar: func(bar input.PeriodBoundaries, decision engine.Decision) {
			hour := int(bar.StorageStart.Sub(origin) / time.Hour)
			phases[hour] = decision.Summary.Pool.Drift.Phase
			ratios[hour] = map[string]string{}
			for _, member := range decision.Summary.Pool.Members {
				ratios[hour][member.ID] = member.Ratio
			}
		}}
	if _, err := replay.RunPool(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	for hour := 85; hour < 90; hour++ {
		if phases[hour] != engine.DriftPending {
			t.Fatalf("第 %d 小时还没到漂移起点：%q", hour, phases[hour])
		}
	}
	if phases[90] != engine.DriftStart || ratios[90]["a"] != "0.7" || ratios[90]["b"] != "0.3" {
		t.Fatalf("initial.ratios 应落在 at 那一根：%q %v", phases[90], ratios[90])
	}
	// 最新已收盘的一根是第 99 小时（bar_end 第 100 小时）。
	if phases[91] != engine.DriftRunning || phases[99] != engine.DriftRunning {
		t.Fatalf("at 之后照常漂移：%q %q", phases[91], phases[99])
	}
	h.manager.Start(context.Background(), "i1", "session-1")
	eventually(t, "预热完成", func() bool { return h.session(t).WarmedThrough != nil })
	if drift := warmedDrift(t, h.session(t)); drift == nil || drift.Start != barIndexAt(t, 90) {
		t.Fatalf("预热状态的漂移起点应是 at：%+v", drift)
	}
}

// 起点早于 View 的实际覆盖：启用时的覆盖核对拒绝（预热会因数据不够失败）。
func TestDriftWarmCoverageRejectsEarlyStart(t *testing.T) {
	h := newPoolHarness(t, driftYaml("    start: \""+hourText(-10)+"\"\n"), "a", "b")
	session := h.session(t)
	resolved, _ := input.ParseResolved([]byte(session.ResolvedJSON))
	members, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dsl.Parse([]byte(driftYaml("    start: \"" + hourText(-10) + "\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	program, err := dsl.Compile(strategy, resolved.ViewColumns)
	if err != nil {
		t.Fatal(err)
	}
	pooled := input.PoolResolved{Resolved: resolved, Program: program, Members: members, Programs: programs}
	err = input.CheckPoolWarmCoverage(context.Background(), h.client, "crypto", pooled, h.manager.Now())
	if err == nil || !strings.Contains(err.Error(), "不能启用") {
		t.Fatalf("起点早于覆盖应拒绝启用：%v", err)
	}
}
