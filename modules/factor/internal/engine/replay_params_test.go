package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 闭环回放（设计 8.4）：每期请求带来的参数快照真正改变计算；没带的期次沿用上一期实际用的参数，再没有就用成员自己的参数；
// 因子帧里带上这一期各标的各成员实际用的参数（与结果行的 __params 一致），按标的覆盖逐标的体现。
func TestReplayStepAppliesRequestedParams(t *testing.T) {
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 6, default: 2}\noutput: ts_mean(close, window)\n")
	set := uncompiled(domain.EngineTask{
		Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
			ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled, BoundInstanceID: "inst"},
		Members: []domain.MemberDef{{Key: "pmean__var", Factor: def, Kind: domain.MemberKindVar, Params: map[string]float64{"window": 2}}}, ResultReady: true,
	})
	m, _ := newReplayManagerWithTask(t, set)
	ctx := context.Background()
	_, _, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "tuned", TaskID: "ftask_replay", From: rt0.Add(14 * time.Minute).Format(time.RFC3339), To: rt0.Add(17 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	at := rt0.Add(15 * time.Minute).Format(time.RFC3339)

	// 默认窗口 2：close 在第 15 分钟是 115，均值 (114+115)/2。
	step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at})
	require.NoError(t, err)
	require.InDelta(t, 114.5, step.Subjects["A"].Factors["pmean__var"]["pmean__var"], 1e-9)
	require.InDelta(t, 2, step.Subjects["A"].Params["pmean__var"]["window"], 1e-9, "没有请求的期次实际用的是成员自己的参数")

	// 请求带来的快照：A 用窗口 4、B 用窗口 2（覆盖按标的）。
	step, _, err = m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at,
		Params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 2, Overrides: map[string]float64{"A": 4}}}}})
	require.NoError(t, err)
	require.InDelta(t, 113.5, step.Subjects["A"].Factors["pmean__var"]["pmean__var"], 1e-9, "A 的窗口是 4：(112+113+114+115)/4")
	require.InDelta(t, 4, step.Subjects["A"].Params["pmean__var"]["window"], 1e-9)
	require.InDelta(t, 114.5, step.Subjects["B"].Factors["pmean__var"]["pmean__var"], 1e-9)
	require.InDelta(t, 2, step.Subjects["B"].Params["pmean__var"]["window"], 1e-9)

	// 没有的成员是调用方的错误：整期失败。
	_, status, err := m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at, Params: map[string]domain.ParamSnapshot{"nope": {"window": {Default: 3}}}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	// 不合法的快照（超出范围）与实时提交被拒绝一样：这个成员沿用上一期实际用的参数，整期不失败，拒绝原因随响应返回，
	// 实际生效的参数也返回（策略回放据此保存，非法快照不会留在记录里）。
	next := rt0.Add(16 * time.Minute).Format(time.RFC3339)
	step, _, err = m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: next, Params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 99}}}})
	require.NoError(t, err)
	require.Len(t, step.RejectedParams, 1)
	require.Equal(t, "pmean__var", step.RejectedParams[0].Member)
	require.NotEmpty(t, step.RejectedParams[0].Reason)
	require.InDelta(t, 4, step.Subjects["A"].Params["pmean__var"]["window"], 1e-9, "沿用上一期实际用的窗口（A 的覆盖 4）")
	require.InDelta(t, 2, step.Subjects["B"].Params["pmean__var"]["window"], 1e-9)
	require.Equal(t, 2.0, step.AppliedParams["pmean__var"]["window"].Default)
	require.Equal(t, 4.0, step.AppliedParams["pmean__var"]["window"].Overrides["A"])

	// 合法的请求没有拒绝，实际生效的就是请求的。
	step, _, err = m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: rt0.Add(17 * time.Minute).Add(-time.Minute).Format(time.RFC3339),
		Params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 3}}}})
	require.NoError(t, err)
	require.Empty(t, step.RejectedParams)
	require.Equal(t, 3.0, step.AppliedParams["pmean__var"]["window"].Default)
}

// 固定成员：回放按固定值计算，请求里逐期提交的参数被拒绝并沿用固定值；帧里的参数就是固定值。
func TestReplayFixedParamMemberIgnoresRequestedParams(t *testing.T) {
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 6, default: 2}\noutput: ts_mean(close, window)\n")
	set := uncompiled(domain.EngineTask{
		Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
			ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled},
		Members: []domain.MemberDef{{Key: "pmean__4", Factor: def, Kind: domain.MemberKindFixed, Params: map[string]float64{"window": 4}}}, ResultReady: true,
	})
	m, _ := newReplayManagerWithTask(t, set)
	ctx := context.Background()
	_, _, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "fixed", TaskID: "ftask_replay", From: rt0.Add(14 * time.Minute).Format(time.RFC3339), To: rt0.Add(17 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	at := rt0.Add(15 * time.Minute).Format(time.RFC3339)

	step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "fixed", Period: at})
	require.NoError(t, err)
	require.InDelta(t, 113.5, step.Subjects["A"].Factors["pmean__4"]["pmean__4"], 1e-9, "窗口 4：(112+113+114+115)/4")
	require.InDelta(t, 4, step.Subjects["A"].Params["pmean__4"]["window"], 1e-9, "固定成员的参数也如实带在帧里")

	step, _, err = m.Step(ctx, ReplayStepRequest{ReplayID: "fixed", Period: at,
		Params: map[string]domain.ParamSnapshot{"pmean__4": {"window": {Default: 2}}}})
	require.NoError(t, err)
	require.Len(t, step.RejectedParams, 1)
	require.Contains(t, step.RejectedParams[0].Reason, "固定成员")
	require.InDelta(t, 113.5, step.Subjects["A"].Factors["pmean__4"]["pmean__4"], 1e-9, "仍按固定值计算")
}
