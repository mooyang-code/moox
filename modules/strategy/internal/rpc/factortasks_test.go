package rpc

import (
	"errors"
	"strings"
	"testing"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

const tunedDSL = `name: tuned
factor_params:
  bias__var:
    window: 20
rules:
  - id: r
    type: rank
    score: "bias__var"
    select: {top: 1}
    weight: 1
`

// 实例创建时必须选定一个合法的因子任务；一个任务只能被一个实例独占；任务在创建时写定。
func TestInstanceFactorTaskIsRequiredAndExclusive(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	missing := h.createInstance("i1", "s1", "", "", false)
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "factor_tasks")
	badID := h.createInstance("i1", "s1", "view_a", "", false)
	requireFail(t, int32(badID.GetRetInfo().GetCode()), badID.GetRetInfo().GetMsg(), "不合法")

	created := h.createInstance("i1", "s1", "ftask_shared", "", false)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	if created.GetInstance().GetFactorTasks()["spot"] != "ftask_shared" {
		t.Fatalf("实例应记下因子任务：%+v", created.GetInstance())
	}
	taken := h.createInstance("i2", "s1", "ftask_shared", "", false)
	requireFail(t, int32(taken.GetRetInfo().GetCode()), taken.GetRetInfo().GetMsg(), "")
	if _, err := h.service.Store.GetInstance(h.ctx, "i2"); err == nil {
		t.Fatal("任务已被独占时第二个实例不应创建成功")
	}

	// 修改实例：任务不可改，留空或给同一个任务则只改其他字段。
	changed, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_other"}}})
	requireFail(t, int32(changed.GetRetInfo().GetCode()), changed.GetRetInfo().GetMsg(), "不能修改")
	same, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_shared"}, LogicalAccountId: "acct-1"}})
	requireOK(t, int32(same.GetRetInfo().GetCode()), same.GetRetInfo().GetMsg())
	if same.GetInstance().GetFactorTasks()["spot"] != "ftask_shared" || same.GetInstance().GetLogicalAccountId() != "acct-1" {
		t.Fatalf("修改后的实例不符：%+v", same.GetInstance())
	}
}

// 启用时以实例和任务调用解析（关联任务）；会话固化任务的结果 View；重复启用沿用会话，停用后再启用重新解析。
func TestEnableBindsFactorTaskAndSnapshotsItsView(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	instance := created.GetInstance()
	if !instance.GetEnabled() || len(h.resolver.bound) != 1 || h.resolver.bound[0] != "i1@ftask_i1" {
		t.Fatalf("启用应关联任务：%+v bound=%v", instance, h.resolver.bound)
	}
	if !strings.Contains(instance.GetResolvedJson(), `"view_id":"view_factor_i1"`) || !strings.Contains(instance.GetResolvedJson(), `"factor_task_id":"ftask_i1"`) {
		t.Fatalf("会话应固化任务与它的结果 View：%s", instance.GetResolvedJson())
	}
	again := h.setEnabled("i1", true)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetSessionId() != instance.GetSessionId() || len(h.resolver.bound) != 1 {
		t.Fatalf("重复启用应沿用会话：%+v bound=%v", again.GetInstance(), h.resolver.bound)
	}
	requireOK(t, int32(h.setEnabled("i1", false).GetRetInfo().GetCode()), "")
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if len(h.resolver.bound) != 2 || reenabled.GetInstance().GetSessionId() == instance.GetSessionId() {
		t.Fatalf("停用后再启用应重新解析并新建会话：bound=%v", h.resolver.bound)
	}
}

// 校验与研究回放都按因子任务解析；回放必须给出任务；输出了 factor_params 的策略暂不支持研究回放。
func TestValidateAndReplayByFactorTask(t *testing.T) {
	h := newHarness(t)
	h.resolver.columns = []string{"close", "bias__var"}
	validated, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: tunedDSL, FactorTasks: map[string]string{"spot": "ftask_x"}})
	requireOK(t, int32(validated.GetRetInfo().GetCode()), validated.GetRetInfo().GetMsg())
	if !strings.Contains(validated.GetResolvedJson(), `"factor_task_id":"ftask_x"`) || !strings.Contains(validated.GetResolvedJson(), `"view_id":"view_factor_x"`) {
		t.Fatalf("校验应按任务解析并返回绑定：%s", validated.GetResolvedJson())
	}
	if len(h.resolver.bound) != 0 {
		t.Fatalf("校验只读，不能关联任务：%v", h.resolver.bound)
	}

	noTask, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: demoDSL, StartTime: "2026-09-01T00:00:00Z", EndTime: "2026-09-02T00:00:00Z"})
	requireFail(t, int32(noTask.GetRetInfo().GetCode()), noTask.GetRetInfo().GetMsg(), "factor_tasks")
	tuned, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: tunedDSL, FactorTasks: map[string]string{"spot": "ftask_x"}, StartTime: "2026-09-01T00:00:00Z", EndTime: "2026-09-02T00:00:00Z"})
	requireFail(t, int32(tuned.GetRetInfo().GetCode()), tuned.GetRetInfo().GetMsg(), "factor_params")
}

func TestDisplaySettingsRequireInScopeInstance(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i1", "s1", "ftask_i1", "", false).GetRetInfo().GetCode()), "")
	set, _ := h.service.SetStrategyDisplaySettings(h.ctx, &strategypb.SetStrategyDisplaySettingsReq{InstanceId: "i1", Hidden: &strategypb.DisplayHidden{Categories: []string{"volume"}, Members: []string{"bias__var"}, Columns: []string{"bias__var"}}})
	requireOK(t, int32(set.GetRetInfo().GetCode()), set.GetRetInfo().GetMsg())
	got, _ := h.service.GetStrategyDisplaySettings(h.ctx, &strategypb.GetStrategyDisplaySettingsReq{InstanceId: "i1"})
	if len(got.GetHidden().GetCategories()) != 1 || got.GetHidden().GetColumns()[0] != "bias__var" || got.GetHidden().GetMembers()[0] != "bias__var" {
		t.Fatalf("外显设置读回不符：%+v", got.GetHidden())
	}
	missing, _ := h.service.GetStrategyDisplaySettings(h.ctx, &strategypb.GetStrategyDisplaySettingsReq{InstanceId: "nope"})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "")
}

// factor_tasks 的写法：一个或两个，市场只能是 spot、swap、equity，任务不能重复，股票不能与加密货币混用。
func TestCreateInstanceFactorTasksRules(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	create := func(id string, tasks map[string]string) *strategypb.CreateStrategyInstanceRsp {
		rsp, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: id, StrategyId: "s1", FactorTasks: tasks}})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	for name, tc := range map[string]struct {
		tasks map[string]string
		want  string
	}{
		"未知市场":  {map[string]string{"futures": "ftask_a"}, "不受支持"},
		"任务重复":  {map[string]string{"spot": "ftask_a", "swap": "ftask_a"}, "同时登记"},
		"股票不混用": {map[string]string{"equity": "ftask_a", "spot": "ftask_b"}, "不能与现货、永续混用"},
	} {
		rsp := create("bad-"+name, tc.tasks)
		requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), tc.want)
	}
	both := create("i-both", map[string]string{"spot": "ftask_spot", "swap": "ftask_swap"})
	requireOK(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg())
	if tasks := both.GetInstance().GetFactorTasks(); tasks["spot"] != "ftask_spot" || tasks["swap"] != "ftask_swap" || len(tasks) != 2 {
		t.Fatalf("实例应记下两个因子任务：%+v", both.GetInstance())
	}
	// 一个任务只能被一个实例独占，即使另一个任务不同。
	taken := create("i-other", map[string]string{"spot": "ftask_x", "swap": "ftask_swap"})
	requireFail(t, int32(taken.GetRetInfo().GetCode()), taken.GetRetInfo().GetMsg(), "")
	if _, err := h.service.Store.GetInstance(h.ctx, "i-other"); err == nil {
		t.Fatal("任务已被独占时第二个实例不应创建成功")
	}
	// 同一个 instance_id、相同任务重试幂等；任务不同则冲突。
	again := create("i-both", map[string]string{"swap": "ftask_swap", "spot": "ftask_spot"})
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	conflict := create("i-both", map[string]string{"spot": "ftask_spot"})
	requireFail(t, int32(conflict.GetRetInfo().GetCode()), conflict.GetRetInfo().GetMsg(), "冲突")
}

// 校验接口按市场先后把全部绑定的任务交给解析（试算读全部 View）。
func TestValidateStrategyReadsEveryBoundTask(t *testing.T) {
	h := newHarness(t)
	rsp, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: demoDSL, FactorTasks: map[string]string{"swap": "ftask_b", "spot": "ftask_a"}})
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if got := strings.Join(h.resolver.resolvedTasks, ","); got != "ftask_a,ftask_b" {
		t.Fatalf("应按现货、永续的顺序解析全部任务：%s", got)
	}
}

// 创建实例时就核对“市场 → 任务”：登记错市场的任务不会被占住，改对之后可以再创建。
func TestCreateInstanceChecksTaskMarkets(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	create := func(id string, tasks map[string]string) *strategypb.CreateStrategyInstanceRsp {
		rsp, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: id, StrategyId: "s1", FactorTasks: tasks}})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	h.resolver.taskMarkets = map[string]string{"ftask_real_swap": "swap"}
	wrong := create("i1", map[string]string{"spot": "ftask_real_swap"})
	requireFail(t, int32(wrong.GetRetInfo().GetCode()), wrong.GetRetInfo().GetMsg(), "登记为 spot 市场，但它的源数据集是 swap 市场")
	if _, err := h.service.Store.GetInstance(h.ctx, "i1"); err == nil {
		t.Fatal("市场登记错误时不应创建实例")
	}
	right := create("i1", map[string]string{"swap": "ftask_real_swap"})
	requireOK(t, int32(right.GetRetInfo().GetCode()), right.GetRetInfo().GetMsg())
	h.resolver.taskMarketErr = errors.New("View 不存在")
	h.resolver.taskMarkets = nil
	unknown := create("i2", map[string]string{"spot": "ftask_new"})
	requireFail(t, int32(unknown.GetRetInfo().GetCode()), unknown.GetRetInfo().GetMsg(), "无法确定因子任务 ftask_new 的市场")
}

// factor_tasks 的 multi 键：统一数据集上的任务只能单独绑定。
func TestNormalizeFactorTasksUnified(t *testing.T) {
	tasks, err := normalizeFactorTasks(map[string]string{"multi": " ftask_unified "})
	if err != nil || tasks["multi"] != "ftask_unified" || len(tasks) != 1 {
		t.Fatalf("multi 单独绑定应通过：%v err=%v", tasks, err)
	}
	for name, raw := range map[string]map[string]string{
		"与现货混用": {"multi": "ftask_a", "spot": "ftask_b"},
		"与永续混用": {"multi": "ftask_a", "swap": "ftask_b"},
		"未知市场":  {"future": "ftask_a"},
	} {
		if _, err := normalizeFactorTasks(raw); err == nil {
			t.Errorf("%s应被拒绝", name)
		}
	}
}
