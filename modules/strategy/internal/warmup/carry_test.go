package warmup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/replay"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 本文件验证策略池重新启用时规则状态的沿用：DSL、绑定与成员都没变时从沿用的状态接着推进（不重新预热），
// 停用太久或状态不可用时退回从头预热。

func hoursAt(hours float64) func() time.Time {
	at := origin.Add(time.Duration(hours * float64(time.Hour)))
	return func() time.Time { return at }
}

// reenable 停用实例再启用，产生新会话：快照（DSL 版本、绑定解析、成员）与上一个会话相同。
func (h *harness) reenable(t *testing.T, previous, next string) {
	t.Helper()
	ctx := context.Background()
	old, err := h.repo.GetSession(ctx, previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.DisableInstance(ctx, "i1", previous, origin); err != nil {
		t.Fatal(err)
	}
	h.manager.Stop("i1")
	if err := h.repo.EnableInstance(ctx, store.Session{SessionID: next, InstanceID: "i1", DSLHash: old.DSLHash, ResolvedJSON: old.ResolvedJSON, MembersJSON: old.MembersJSON, CreatedAt: origin}, poolYaml); err != nil {
		t.Fatal(err)
	}
}

// warmed 等会话的预热完成并返回它。
func (h *harness) warmed(t *testing.T, sessionID string) store.Session {
	t.Helper()
	var session store.Session
	eventually(t, "预热完成", func() bool {
		var err error
		session, err = h.repo.GetSession(context.Background(), sessionID)
		return err == nil && session.WarmedThrough != nil
	})
	return session
}

// freshRun 是对照：按 startNow 时刻的预热起点，从空状态一路推进到 endNow 时刻最新已收盘的一期，返回最终状态的 JSON。
func (h *harness) freshRun(t *testing.T, session store.Session, startNow, endNow time.Time) string {
	t.Helper()
	resolved, err := input.ParseResolved([]byte(session.ResolvedJSON))
	if err != nil {
		t.Fatal(err)
	}
	members, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := dsl.Parse([]byte(poolYaml))
	if err != nil {
		t.Fatal(err)
	}
	clock, err := input.PoolClock(resolved.Calendar, resolved.Bar, *strategy.Pool)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pool.NewConfig(strategy, resolved.Market, input.MemberStrategies(members, programs), clock)
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := Window(resolved, strategy, programs, startNow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, end, err := Window(resolved, strategy, programs, endNow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := replay.RunPool(context.Background(), replay.PoolRun{Client: &fakeClient{}, SpaceID: "crypto", Resolved: resolved, Members: members, Programs: programs, Config: cfg,
		Start: start, End: end, IncludeLatest: true, ChunkBars: 4, Now: func() time.Time { return endNow }})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.State)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 停用期间漏掉了 3 根：重新启用后从沿用的状态接着推进这 3 根，结果与从原来的预热起点一路推进到现在逐字节一致
// （账本、净值序列都连续），而不是从新的预热起点重新开始。
func TestReenabledPoolContinuesFromCarriedState(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	first := h.warmed(t, "session-1")
	before := h.state(t)
	if !before.BarEndTime.Equal(origin.Add(100 * time.Hour)) {
		t.Fatalf("第一次预热的终点 %v", before.BarEndTime)
	}

	h.reenable(t, "session-1", "session-2")
	if carried := h.state(t); carried.SessionID != "session-2" || !carried.BarEndTime.Equal(before.BarEndTime) || string(carried.StateJSON) != string(before.StateJSON) {
		t.Fatalf("快照没变，状态应原样挂到新会话：%+v", carried)
	}
	h.manager.Now = hoursAt(103.5)
	h.client.mu.Lock()
	queries := h.client.queries
	h.client.mu.Unlock()
	h.manager.Start(context.Background(), "i1", "session-2")
	second := h.warmed(t, "session-2")
	if !second.WarmedThrough.Equal(origin.Add(103 * time.Hour)) {
		t.Fatalf("接着推进的终点 %v，期望第 103 小时", second.WarmedThrough)
	}
	after := h.state(t)
	if after.SessionID != "session-2" || !after.BarEndTime.Equal(origin.Add(103*time.Hour)) {
		t.Fatalf("状态应推进到第 103 小时：%+v", after)
	}
	if want := h.freshRun(t, first, origin.Add(100*time.Hour+30*time.Minute), origin.Add(103*time.Hour+30*time.Minute)); string(after.StateJSON) != want {
		t.Fatalf("接着推进应与从原起点一路推进一致：\n接着 %s\n一路 %s", after.StateJSON, want)
	}
	// 从新的预热起点重新开始得不到同样的状态（净值序列从 1 重新起步），说明确实是沿用。
	if fresh := h.freshRun(t, first, origin.Add(103*time.Hour+30*time.Minute), origin.Add(103*time.Hour+30*time.Minute)); string(after.StateJSON) == fresh {
		t.Fatal("对照无效：从新起点重新预热的状态与沿用的相同")
	}
	h.client.mu.Lock()
	read := h.client.queries - queries
	h.client.mu.Unlock()
	if read == 0 {
		t.Fatal("接着推进应读取漏掉的那几根")
	}
	eventually(t, "门禁放行", func() bool { return h.manager.Blocked("i1") == "" })
}

// 重新启用时还没有新收盘的期：不读历史、不推进，状态原样沿用，预热终点就是状态所在的那一期。
func TestReenabledPoolWithNoMissedBarsKeepsState(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	h.warmed(t, "session-1")
	before := h.state(t)
	h.reenable(t, "session-1", "session-2")
	h.client.mu.Lock()
	queries := h.client.queries
	h.client.mu.Unlock()
	h.manager.Start(context.Background(), "i1", "session-2")
	second := h.warmed(t, "session-2")
	after := h.state(t)
	if !second.WarmedThrough.Equal(before.BarEndTime) || after.SessionID != "session-2" || string(after.StateJSON) != string(before.StateJSON) {
		t.Fatalf("没有漏期时状态应原样沿用：%+v", after)
	}
	h.client.mu.Lock()
	defer h.client.mu.Unlock()
	if h.client.queries != queries {
		t.Fatalf("没有漏期时不应读历史：%d → %d", queries, h.client.queries)
	}
}

// 状态之后那一根已经收盘超过宽限，但 View 还没写到它：当作没有新收盘的根，状态原样沿用，不退回从头预热；
// 那一根等它的就绪事件照常处理。
func TestReenabledPoolKeepsStateWhenViewLagsBehind(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	h.warmed(t, "session-1")
	before := h.state(t)
	h.reenable(t, "session-1", "session-2")
	// 现在是第 101.5 小时：bar_end = 第 101 小时的那根已收盘，但索引里最晚一行还是 bar_start = 第 99 小时。
	h.manager.Now = hoursAt(101.5)
	h.client.indexedTo = origin.Add(99 * time.Hour)
	h.manager.Start(context.Background(), "i1", "session-2")
	second := h.warmed(t, "session-2")
	after := h.state(t)
	if !second.WarmedThrough.Equal(before.BarEndTime) || after.SessionID != "session-2" || string(after.StateJSON) != string(before.StateJSON) {
		t.Fatalf("View 还没写到下一根时状态应原样沿用：终点 %v 状态 %+v", second.WarmedThrough, after)
	}
}

// 停用期间收盘的那一根正好是 View 的最新一根：它的就绪事件已在停用期间被确认掉，接着推进必须把它补上，
// 结果与从原起点一路推进到这一根逐字节一致；不能当成“View 还没写到”而漏掉。
func TestReenabledPoolPushesLatestBarInView(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	first := h.warmed(t, "session-1")
	h.reenable(t, "session-1", "session-2")
	// 现在是第 101.5 小时，View 最新一行是 bar_start = 第 100 小时，即 bar_end = 第 101 小时的那根。
	h.manager.Now = hoursAt(101.5)
	h.client.indexedTo = origin.Add(100 * time.Hour)
	h.manager.Start(context.Background(), "i1", "session-2")
	second := h.warmed(t, "session-2")
	if !second.WarmedThrough.Equal(origin.Add(101 * time.Hour)) {
		t.Fatalf("View 里已有的第 101 小时那根应被接着推进：终点 %v", second.WarmedThrough)
	}
	after := h.state(t)
	if !after.BarEndTime.Equal(origin.Add(101 * time.Hour)) {
		t.Fatalf("状态应推进到第 101 小时：%+v", after)
	}
	if want := h.freshRun(t, first, origin.Add(100*time.Hour+30*time.Minute), origin.Add(101*time.Hour+30*time.Minute)); string(after.StateJSON) != want {
		t.Fatalf("接着推进应与从原起点一路推进一致：\n接着 %s\n一路 %s", after.StateJSON, want)
	}

	// 对照：同样的 View 状态下第一次启用（从头预热）也推进到这一根。
	fresh := newHarness(t)
	fresh.manager.Now = hoursAt(101.5)
	fresh.client.indexedTo = origin.Add(100 * time.Hour)
	fresh.manager.Start(context.Background(), "i1", "session-1")
	if through := fresh.warmed(t, "session-1").WarmedThrough; !through.Equal(*second.WarmedThrough) {
		t.Fatalf("从头预热的终点 %v 应与接着推进的终点 %v 相同", through, second.WarmedThrough)
	}
}

// 停用太久：沿用的状态早于从头预热的起点（漏掉的期数超过一次预热要推进的期数），清空并从头预热。
func TestReenabledPoolRewarmsWhenStateIsOlderThanWarmWindow(t *testing.T) {
	h := newHarness(t)
	h.manager.Start(context.Background(), "i1", "session-1")
	first := h.warmed(t, "session-1")
	h.reenable(t, "session-1", "session-2")
	h.manager.Now = hoursAt(200.5)
	h.manager.Start(context.Background(), "i1", "session-2")
	second := h.warmed(t, "session-2")
	if !second.WarmedThrough.Equal(origin.Add(200 * time.Hour)) {
		t.Fatalf("预热终点 %v", second.WarmedThrough)
	}
	after := h.state(t)
	if want := h.freshRun(t, first, origin.Add(200*time.Hour+30*time.Minute), origin.Add(200*time.Hour+30*time.Minute)); string(after.StateJSON) != want {
		t.Fatalf("停用太久应从头预热：\n实际 %s\n期望 %s", after.StateJSON, want)
	}
}

// 沿用的状态无法解析，或接着推进失败：退回从头预热，不让实例卡住。
func TestReenabledPoolRewarmsWhenCarriedStateIsUnusable(t *testing.T) {
	for name, broken := range map[string]string{"无法解析": `{"rules": 1}`, "池状态损坏": `{"pool": "x"}`} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.manager.Start(context.Background(), "i1", "session-1")
			first := h.warmed(t, "session-1")
			h.reenable(t, "session-1", "session-2")
			if err := h.repo.ExecForTest(`UPDATE t_strategy_states SET c_state_json = '` + broken + `' WHERE c_instance_id = 'i1';`); err != nil {
				t.Fatal(err)
			}
			h.manager.Now = hoursAt(103.5)
			h.manager.Start(context.Background(), "i1", "session-2")
			h.warmed(t, "session-2")
			after := h.state(t)
			if want := h.freshRun(t, first, origin.Add(103*time.Hour+30*time.Minute), origin.Add(103*time.Hour+30*time.Minute)); string(after.StateJSON) != want {
				t.Fatalf("状态不可用时应从头预热：\n实际 %s\n期望 %s", after.StateJSON, want)
			}
		})
	}
}
