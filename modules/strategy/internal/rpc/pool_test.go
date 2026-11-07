package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

const poolRules = `rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: 1
`

const (
	spotMemberDSL = "name: spot_member\nbar: 1h\nmarket: {select: spot}\n" + poolRules
	swapMemberDSL = "name: swap_member\nbar: 1h\nmarket: {select: swap}\n" + poolRules
	poolDefDSL    = `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: member_spot}
    - {id: b, strategy: member_swap}
  allocation: {method: fixed, ratios: {a: 0.6, b: 0.4}}
`
)

// ResolvePool 按成员的市场构造池的绑定（假解析器不读 Storage）。
func (f *fakeResolver) ResolvePool(_ context.Context, _ string, taskIDs []string, pool dsl.Strategy, members []input.PoolMemberDef) (input.PoolResolved, error) {
	f.calls++
	f.resolvedTasks = taskIDs
	if f.err != nil {
		return input.PoolResolved{}, f.err
	}
	strategies := map[string]dsl.Strategy{}
	for _, member := range members {
		strategies[member.ID] = member.Strategy
	}
	union, err := dsl.ValidatePoolMembers(pool, strategies)
	if err != nil {
		return input.PoolResolved{}, err
	}
	if len(taskIDs) != len(union.Markets()) {
		return input.PoolResolved{}, errors.New("策略池的成员需要绑定 " + strings.Join(union.Markets(), "、") + " 的因子任务")
	}
	columns := []string{"close"}
	tasks := map[string]input.TaskBinding{}
	for i, id := range taskIDs {
		viewID, err := input.ViewIDOfTask(id)
		if err != nil {
			return input.PoolResolved{}, err
		}
		market := union.Markets()[i]
		tasks[market] = input.TaskBinding{ViewID: viewID, FactorTaskID: id, DatasetID: "ds"}
	}
	program, err := dsl.Compile(pool, columns)
	if err != nil {
		return input.PoolResolved{}, err
	}
	out := input.PoolResolved{Resolved: input.Resolved{Market: union, Tasks: tasks, Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: columns}, Program: program}
	for _, member := range members {
		memberTasks := map[string]input.TaskBinding{}
		for _, market := range member.Strategy.Market.Markets() {
			memberTasks[market] = tasks[market]
		}
		compiled, err := dsl.Compile(member.Strategy, columns)
		if err != nil {
			return input.PoolResolved{}, err
		}
		out.Members = append(out.Members, input.PoolMember{ID: member.ID, StrategyID: member.StrategyID, DSLHash: member.DSLHash, DSLYaml: member.DSLYaml,
			Resolved: input.Resolved{Market: member.Strategy.Market, Tasks: memberTasks, Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: columns}})
		out.Programs = append(out.Programs, compiled)
	}
	return out, nil
}

func (f *fakeResolver) ResolvePoolInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, pool dsl.Strategy, members []input.PoolMemberDef) (input.PoolResolved, error) {
	f.bound = append(f.bound, instanceID+"@"+strings.Join(input.TaskIDs(tasks), "+"))
	return f.ResolvePool(ctx, spaceID, input.TaskIDs(tasks), pool, members)
}

func (f *fakeResolver) CheckPoolCoverage(context.Context, string, []input.PoolMember, time.Time) error {
	return f.coverageErr
}

func createPoolInstance(h *harness, id, strategyID string, tasks map[string]string, enabled bool) *strategypb.CreateStrategyInstanceRsp {
	h.t.Helper()
	rsp, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: id, StrategyId: strategyID, FactorTasks: tasks, Enabled: enabled}})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

func TestPoolDefinitionChecksMembers(t *testing.T) {
	h := newHarness(t)
	createPool := func(raw string) *strategypb.CreateStrategyRsp {
		rsp, err := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{StrategyId: "pool_1", DslYaml: raw}})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	missing := createPool(poolDefDSL)
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "成员 a 引用的策略 member_spot 不存在")
	h.createStrategy("member_spot", spotMemberDSL)
	stillMissing := createPool(poolDefDSL)
	requireFail(t, int32(stillMissing.GetRetInfo().GetCode()), stillMissing.GetRetInfo().GetMsg(), "成员 b 引用的策略 member_swap 不存在")
	h.createStrategy("no_market", "name: no_market\nbar: 1h\n"+poolRules)
	h.createStrategy("other_bar", "name: other_bar\nbar: 5m\nmarket: {select: swap}\n"+poolRules)
	noMarket := createPool(strings.Replace(poolDefDSL, "member_swap", "no_market", 1))
	requireFail(t, int32(noMarket.GetRetInfo().GetCode()), noMarket.GetRetInfo().GetMsg(), "没有显式写 market")
	otherBar := createPool(strings.Replace(poolDefDSL, "member_swap", "other_bar", 1))
	requireFail(t, int32(otherBar.GetRetInfo().GetCode()), otherBar.GetRetInfo().GetMsg(), "与池的 bar 1h 不一致")
	h.createStrategy("member_swap", swapMemberDSL)
	created := createPool(poolDefDSL)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	// 池不能嵌套：引用池的池被拒绝。
	nested := strings.Replace(poolDefDSL, "member_swap", "pool_1", 1)
	rsp, _ := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{StrategyId: "pool_2", DslYaml: nested}})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "不能嵌套")
	// 修改定义时同样检查成员。
	update, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "pool_1", DslYaml: strings.Replace(poolDefDSL, "member_spot", "ghost", 1)})
	requireFail(t, int32(update.GetRetInfo().GetCode()), update.GetRetInfo().GetMsg(), "ghost 不存在")
}

func TestPoolEnableFreezesMembers(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	// 任务并集不齐：成员需要现货与永续，只绑一个现货任务。
	one := createPoolInstance(h, "p_one", "pool_1", map[string]string{"spot": "ftask_p1"}, true)
	requireFail(t, int32(one.GetRetInfo().GetCode()), one.GetRetInfo().GetMsg(), "")
	created := createPoolInstance(h, "p1", "pool_1", map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}, true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	instance := created.GetInstance()
	if !instance.GetEnabled() || instance.GetSessionId() == "" {
		t.Fatalf("池实例应已启用：%+v", instance)
	}
	session, err := h.service.Store.GetSession(h.ctx, instance.GetSessionId())
	if err != nil {
		t.Fatal(err)
	}
	members, programs, err := input.ParseMembers(session.MembersJSON)
	if err != nil || len(members) != 2 || len(programs) != 2 {
		t.Fatalf("会话应固化成员：%q %v", session.MembersJSON, err)
	}
	if members[0].ID != "a" || members[0].StrategyID != "member_spot" || members[0].DSLYaml != spotMemberDSL || members[0].DSLHash != dsl.Hash([]byte(spotMemberDSL)) || members[1].DSLYaml != swapMemberDSL {
		t.Fatalf("固化的成员不符：%+v", members)
	}
	if session.WarmedThrough != nil || session.WarmState != "" {
		t.Fatalf("预热结果在预热完成前为空：%+v", session)
	}
	if len(h.owner.claims) != 0 {
		t.Fatalf("没有挂账户的观察实例不认领会话：%v", h.owner.claims)
	}
	// 成员定义之后再改，不影响已启用的池：重复启用沿用同一会话与固化的成员。
	if _, err := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "member_spot", DslYaml: strings.Replace(spotMemberDSL, "name: spot_member", "name: spot_member2", 1)}); err != nil {
		t.Fatal(err)
	}
	again := h.setEnabled("p1", true)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	reloaded, _ := h.service.Store.GetSession(h.ctx, instance.GetSessionId())
	if again.GetInstance().GetSessionId() != instance.GetSessionId() || reloaded.MembersJSON != session.MembersJSON {
		t.Fatal("成员固化后不随成员定义的修改而变")
	}
}

func TestPoolIsNotReplayableOrTrialled(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	replay, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(replay.GetRetInfo().GetCode()), replay.GetRetInfo().GetMsg(), "研究回放暂不支持策略池")
	validated, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: poolDefDSL, FactorTasks: map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}})
	requireOK(t, int32(validated.GetRetInfo().GetCode()), validated.GetRetInfo().GetMsg())
	if validated.GetResolvedJson() == "" || validated.GetTrial() != nil {
		t.Fatalf("策略池只解析绑定、不试算：%+v", validated)
	}
	missing, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: strings.Replace(poolDefDSL, "member_spot", "ghost", 1)})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "ghost 不存在")
}

type fakeWarmer struct {
	started []string
	stopped []string
}

func (w *fakeWarmer) Start(_ context.Context, instanceID, sessionID string) {
	w.started = append(w.started, instanceID+"@"+sessionID)
}

func (w *fakeWarmer) Stop(instanceID string) { w.stopped = append(w.stopped, instanceID) }

// 池实例启用成功后开始预热，停用时取消；非池实例不预热；重复启用不重新预热。
func TestPoolEnableStartsWarmUpAndDisableStopsIt(t *testing.T) {
	h := newHarness(t)
	warmer := &fakeWarmer{}
	h.service.Warmer = warmer
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	h.createStrategy("plain", demoDSL)
	created := createPoolInstance(h, "p1", "pool_1", map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}, true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	session := created.GetInstance().GetSessionId()
	if len(warmer.started) != 1 || warmer.started[0] != "p1@"+session {
		t.Fatalf("池实例启用后应开始预热：%v", warmer.started)
	}
	again := h.setEnabled("p1", true)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if len(warmer.started) != 1 {
		t.Fatalf("重复启用不重新预热：%v", warmer.started)
	}
	plain := h.createInstance("i1", "plain", "ftask_i1", "", true)
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	if len(warmer.started) != 1 {
		t.Fatalf("非池实例不预热：%v", warmer.started)
	}
	disabled := h.setEnabled("p1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	if len(warmer.stopped) != 1 || warmer.stopped[0] != "p1" {
		t.Fatalf("停用应取消预热：%v", warmer.stopped)
	}
	// 重新启用产生新会话，重新预热。
	reenabled := h.setEnabled("p1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if len(warmer.started) != 2 || warmer.started[1] == warmer.started[0] {
		t.Fatalf("重新启用应以新会话重新预热：%v", warmer.started)
	}
}
