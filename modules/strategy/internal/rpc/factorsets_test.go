package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

const tunedDSL = `name: tuned
factors:
  fast: {factor: bias}
factor_params:
  fast:
    window: "20"
rules:
  - id: r
    type: rank
    score: "fast_bias"
    select: {top: 1}
    weight: 1
`

// tunedResolver 在 fakeResolver 之上实现 InstanceResolver：声明了 factors 的策略绑定实例的私有 View。
type tunedResolver struct {
	*fakeResolver
	instances []string
}

func (r *tunedResolver) ResolveInstance(_ context.Context, instanceID, _, viewID string, strategy dsl.Strategy) (input.Resolved, *dsl.Program, error) {
	r.instances = append(r.instances, instanceID)
	program, err := dsl.Compile(strategy, []string{"close", "fast_bias", "fast__window"})
	if err != nil {
		return input.Resolved{}, nil, err
	}
	return input.Resolved{
		CompletionKind: "factor_period.computed", ViewID: "view_factor_" + instanceID, SourceViewID: viewID, FactorSetID: "fset_" + instanceID, DatasetID: "ds", Bar: "1h",
		Calendar: input.DefaultCalendar, Spot: true, MarketType: "spot", Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "fast_bias"},
	}, program, nil
}

type factorSetsFake struct {
	paused   []string
	released []string
	err      error
}

func (f *factorSetsFake) EnsureInstanceFactorSet(context.Context, input.InstanceFactorSetRequest) (input.InstanceFactorSet, error) {
	return input.InstanceFactorSet{}, nil
}
func (f *factorSetsFake) SetInstanceFactorSetEnabled(_ context.Context, instanceID string, enabled bool) error {
	if !enabled {
		f.paused = append(f.paused, instanceID)
	}
	return f.err
}
func (f *factorSetsFake) ReleaseInstanceFactorSet(_ context.Context, instanceID string) error {
	if f.err != nil {
		return f.err
	}
	f.released = append(f.released, instanceID)
	return nil
}
func (f *factorSetsFake) SubmitFactorParams(context.Context, input.ParamSubmission) ([]input.MemberSubmitResult, error) {
	return nil, nil
}

// 声明了 factors 的实例：启用时按实例建私有因子集并绑定它的 View（会话固化 source_view_id，重复启用沿用会话）；
// 停用时停用私有因子集；删除时先删私有因子集（失败则实例保持原样可重试）；改绑 View 时释放旧的私有因子集。
func TestFactorDeclaringInstanceLifecycle(t *testing.T) {
	h := newHarness(t)
	resolver := &tunedResolver{fakeResolver: h.resolver}
	factors := &factorSetsFake{}
	h.service.Resolver, h.service.Factors = resolver, factors
	h.createStrategy("s1", tunedDSL)

	created := h.createInstance("i1", "s1", "view_a", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	instance := created.GetInstance()
	if !instance.GetEnabled() || instance.GetViewId() != "view_a" || len(resolver.instances) != 1 || resolver.instances[0] != "i1" {
		t.Fatalf("启用结果不符：%+v resolved=%v", instance, resolver.instances)
	}
	session := instance.GetSessionId()
	// 重复启用幂等：沿用会话，不再解析（会话里的绑定是私有 View，实例绑定的仍是 K 线 View）。
	again := h.setEnabled("i1", true)
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetSessionId() != session || len(resolver.instances) != 1 {
		t.Fatalf("重复启用应沿用会话：%+v", again.GetInstance())
	}
	// 停用 → 私有因子集停用；再启用：会话已关闭，重新解析（会重新对齐并启用私有因子集）。
	disabled := h.setEnabled("i1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	if len(factors.paused) != 1 || factors.paused[0] != "i1" {
		t.Fatalf("停用应同步停用私有因子集：%v", factors.paused)
	}
	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if len(resolver.instances) != 2 {
		t.Fatalf("重新启用应重新解析：%v", resolver.instances)
	}

	// 删除：释放私有因子集失败时实例保持原样。
	h.setEnabled("i1", false)
	factors.err = errors.New("factor-mgr 不可达")
	failed, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(failed.GetRetInfo().GetCode()), failed.GetRetInfo().GetMsg(), "私有因子集")
	if got, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"}); got.GetInstance().GetInstanceId() != "i1" {
		t.Fatalf("释放失败时实例应保留：%+v", got)
	}
	factors.err = nil
	deleted, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	if len(factors.released) != 1 || factors.released[0] != "i1" {
		t.Fatalf("删除应释放私有因子集：%v", factors.released)
	}
}

func TestFactorDeclaringStrategyValidateAndReplay(t *testing.T) {
	h := newHarness(t)
	h.service.Resolver = &tunedResolver{fakeResolver: h.resolver}
	validated, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: tunedDSL, ViewId: "view_a"})
	requireOK(t, int32(validated.GetRetInfo().GetCode()), validated.GetRetInfo().GetMsg())
	if len(validated.GetDiagnostics()) != 1 || validated.GetResolvedJson() != "" {
		t.Fatalf("应只校验 DSL 并说明原因：%+v", validated)
	}
	replay, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: tunedDSL, ViewId: "view_a", StartTime: "2026-09-01T00:00:00Z", EndTime: "2026-09-02T00:00:00Z"})
	requireFail(t, int32(replay.GetRetInfo().GetCode()), replay.GetRetInfo().GetMsg(), "factors")
}

func TestFactorSettingsRequireInScopeInstance(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	requireOK(t, int32(h.createInstance("i1", "s1", "view_a", "", false).GetRetInfo().GetCode()), "")
	set, _ := h.service.SetStrategyDisplaySettings(h.ctx, &strategypb.SetStrategyDisplaySettingsReq{InstanceId: "i1", Hidden: &strategypb.DisplayHidden{Categories: []string{"volume"}, Columns: []string{"fast_bias"}}})
	requireOK(t, int32(set.GetRetInfo().GetCode()), set.GetRetInfo().GetMsg())
	got, _ := h.service.GetStrategyDisplaySettings(h.ctx, &strategypb.GetStrategyDisplaySettingsReq{InstanceId: "i1"})
	if len(got.GetHidden().GetCategories()) != 1 || got.GetHidden().GetColumns()[0] != "fast_bias" {
		t.Fatalf("外显设置读回不符：%+v", got.GetHidden())
	}
	missing, _ := h.service.GetStrategyDisplaySettings(h.ctx, &strategypb.GetStrategyDisplaySettingsReq{InstanceId: "nope"})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "")
	list, _ := h.service.ListStrategyParamSubmissions(h.ctx, &strategypb.ListStrategyParamSubmissionsReq{InstanceId: "i1"})
	requireOK(t, int32(list.GetRetInfo().GetCode()), list.GetRetInfo().GetMsg())
}

// 启用中的实例不能删除，也不能改绑；这些请求被拒绝之前不能动它的私有因子集。
func TestRejectedDeleteAndRebindKeepFactorSet(t *testing.T) {
	h := newHarness(t)
	h.service.Resolver = &tunedResolver{fakeResolver: h.resolver}
	factors := &factorSetsFake{}
	h.service.Factors = factors
	h.createStrategy("s1", tunedDSL)
	created := h.createInstance("i1", "s1", "view_a", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())

	rejected, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(rejected.GetRetInfo().GetCode()), rejected.GetRetInfo().GetMsg(), "先停用")
	if len(factors.released) != 0 {
		t.Fatalf("启用中的实例被拒绝删除，不应释放私有因子集：%v", factors.released)
	}

	h.setEnabled("i1", false)
	missing, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{
		InstanceId: "i1", StrategyId: "no-such-strategy", ViewId: "view_b"}})
	requireFail(t, int32(missing.GetRetInfo().GetCode()), missing.GetRetInfo().GetMsg(), "不存在")
	if len(factors.released) != 0 {
		t.Fatalf("改绑到不存在的策略被拒绝，不应释放私有因子集：%v", factors.released)
	}
}

// 别的实例还绑定着私有结果 View 时，删除拥有者被拒绝，私有因子集不会被释放（否则读取方永久失去 View）。
func TestDeleteOwnerRejectedWhileAnotherInstanceReadsItsView(t *testing.T) {
	h := newHarness(t)
	h.service.Resolver = &tunedResolver{fakeResolver: h.resolver}
	factors := &factorSetsFake{}
	h.service.Factors = factors
	h.createStrategy("s1", tunedDSL)
	h.createStrategy("s2", demoDSL)
	created := h.createInstance("i1", "s1", "view_a", "", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	// i2 只读绑定 i1 的私有结果 View（启用时固化的 view_id）。
	reader := h.createInstance("i2", "s2", "view_factor_i1", "", false)
	requireOK(t, int32(reader.GetRetInfo().GetCode()), reader.GetRetInfo().GetMsg())
	h.setEnabled("i1", false)

	rejected, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireFail(t, int32(rejected.GetRetInfo().GetCode()), rejected.GetRetInfo().GetMsg(), "还绑定着")
	if len(factors.released) != 0 {
		t.Fatalf("还有读取方时不应释放私有因子集：%v", factors.released)
	}
	// 读取方删除之后再删拥有者就可以。
	deletedReader, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i2"})
	requireOK(t, int32(deletedReader.GetRetInfo().GetCode()), deletedReader.GetRetInfo().GetMsg())
	deleted, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	if n := len(factors.released); n == 0 || factors.released[n-1] != "i1" {
		t.Fatalf("读取方删除后应释放拥有者的私有因子集：%v", factors.released)
	}
}
