package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/stretchr/testify/require"
)

func replayWindowParams(member string, window float64, overrides map[string]float64) *factorpb.MemberParams {
	return &factorpb.MemberParams{MemberKey: member, Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: window, Overrides: overrides}}}
}

func replayParamOf(row *factorpb.ReplayRow, member, name string) float64 {
	return row.GetParams()[member].GetValues()[name]
}

func replayAppliedOf(period *factorpb.ReplayPeriod, member string) *factorpb.MemberParams {
	for _, item := range period.GetAppliedParams() {
		if item.GetMemberKey() == member {
			return item
		}
	}
	return nil
}

func newTunedReplay(t *testing.T, kind, key string, params map[string]float64) *replayManager {
	t.Helper()
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 6, default: 2}\noutput: ts_mean(close, window)\n")
	set := uncompiled(domain.EngineTask{
		Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
			ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled, BoundInstanceID: "inst"},
		Members: []domain.MemberDef{{Key: key, Factor: def, Kind: kind, Params: params}}, ResultReady: true,
	})
	m, _ := newReplayManagerWithTask(t, set)
	return m
}

// 参数闭环：请求带来的参数快照真正改变计算；没带的期次沿用上一期实际用的参数，再没有就用成员自己的参数；
// 帧里带上这一期各标的各成员实际用的参数，按标的覆盖逐标的体现。
func TestReplayStepAppliesRequestedParams(t *testing.T) {
	m := newTunedReplay(t, domain.MemberKindVar, "pmean__var", map[string]float64{"window": 2})
	mustOpenReplay(t, m, replayOpenReq("tuned", 14, 18, 0, "pmean__var", factorname.ParamsColumn))

	// 默认窗口 2：close 在第 15 分钟是 115，均值 (114+115)/2。
	period := mustStepOne(t, m, "tuned", 15)
	a := replayRowOf(period, "A", plainTag)
	require.InDelta(t, 114.5, a.GetValues()["pmean__var"], 1e-9)
	require.InDelta(t, 2, replayParamOf(a, "pmean__var", "window"), 1e-9, "没有请求的期次实际用的是成员自己的参数")

	// 请求带来的快照：A 用窗口 4、B 用窗口 2（覆盖按标的）。
	period = mustStepOne(t, m, "tuned", 15, replayWindowParams("pmean__var", 2, map[string]float64{"A": 4}))
	a, b := replayRowOf(period, "A", plainTag), replayRowOf(period, "B", plainTag)
	require.InDelta(t, 113.5, a.GetValues()["pmean__var"], 1e-9, "A 的窗口是 4：(112+113+114+115)/4")
	require.InDelta(t, 4, replayParamOf(a, "pmean__var", "window"), 1e-9)
	require.InDelta(t, 114.5, b.GetValues()["pmean__var"], 1e-9)
	require.InDelta(t, 2, replayParamOf(b, "pmean__var", "window"), 1e-9)

	// 没有的成员、重复的成员是调用方的错误：整步失败。
	rsp := stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "tuned", PeriodTime: replayMinuteUnix(15), Periods: 1,
		Params: []*factorpb.MemberParams{replayWindowParams("nope", 3, nil)}})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	rsp = stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "tuned", PeriodTime: replayMinuteUnix(15), Periods: 1,
		Params: []*factorpb.MemberParams{replayWindowParams("pmean__var", 3, nil), replayWindowParams("pmean__var", 4, nil)}})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	// 不合法的快照（超出范围）与实时提交被拒绝一样：这个成员沿用上一期实际用的参数，整期不失败，拒绝原因随响应返回，
	// 实际生效的参数也返回（策略回放据此保存，非法快照不会留在记录里）。
	period = mustStepOne(t, m, "tuned", 16, replayWindowParams("pmean__var", 99, nil))
	require.Len(t, period.GetRejectedParams(), 1)
	require.Equal(t, "pmean__var", period.GetRejectedParams()[0].GetMemberKey())
	require.NotEmpty(t, period.GetRejectedParams()[0].GetReason())
	require.InDelta(t, 4, replayParamOf(replayRowOf(period, "A", plainTag), "pmean__var", "window"), 1e-9, "沿用上一期实际用的窗口（A 的覆盖 4）")
	require.InDelta(t, 2, replayParamOf(replayRowOf(period, "B", plainTag), "pmean__var", "window"), 1e-9)
	applied := replayAppliedOf(period, "pmean__var")
	require.Equal(t, 2.0, applied.GetParams()["window"].GetDefaultValue())
	require.Equal(t, 4.0, applied.GetParams()["window"].GetOverrides()["A"])

	// 合法的请求没有拒绝，实际生效的就是请求的。
	period = mustStepOne(t, m, "tuned", 16, replayWindowParams("pmean__var", 3, nil))
	require.Empty(t, period.GetRejectedParams())
	require.Equal(t, 3.0, replayAppliedOf(period, "pmean__var").GetParams()["window"].GetDefaultValue())
}

// 一步多期时请求的参数只给第一期，后面各期沿用；拒绝只出现在第一期。
func TestReplayMultiPeriodStepCarriesParamsForward(t *testing.T) {
	m := newTunedReplay(t, domain.MemberKindVar, "pmean__var", map[string]float64{"window": 2})
	mustOpenReplay(t, m, replayOpenReq("carry", 14, 18, 0, "pmean__var", factorname.ParamsColumn))
	rsp := stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "carry", PeriodTime: replayMinuteUnix(15), Periods: 3,
		Params: []*factorpb.MemberParams{replayWindowParams("pmean__var", 4, nil)}})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetPeriods(), 3)
	for i, period := range rsp.GetPeriods() {
		a := replayRowOf(period, "A", plainTag)
		require.InDelta(t, 4, replayParamOf(a, "pmean__var", "window"), 1e-9, "第 %d 期沿用第一期的参数", i)
		close := float64(115 + i)
		require.InDelta(t, close-1.5, a.GetValues()["pmean__var"], 1e-9, "窗口 4 的均值")
		require.Equal(t, 4.0, replayAppliedOf(period, "pmean__var").GetParams()["window"].GetDefaultValue())
	}

	rsp = stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "carry", PeriodTime: replayMinuteUnix(16), Periods: 2,
		Params: []*factorpb.MemberParams{replayWindowParams("pmean__var", 99, nil)}})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetPeriods()[0].GetRejectedParams(), 1)
	require.Empty(t, rsp.GetPeriods()[1].GetRejectedParams())
	require.InDelta(t, 4, replayParamOf(replayRowOf(rsp.GetPeriods()[1], "A", plainTag), "pmean__var", "window"), 1e-9)
}

// 固定成员：回放按固定值计算，请求里逐期提交的参数被拒绝并沿用固定值；帧里的参数就是固定值。
func TestReplayFixedParamMemberIgnoresRequestedParams(t *testing.T) {
	m := newTunedReplay(t, domain.MemberKindFixed, "pmean__4", map[string]float64{"window": 4})
	mustOpenReplay(t, m, replayOpenReq("fixed", 14, 17, 0, "pmean__4", factorname.ParamsColumn))

	a := replayRowOf(mustStepOne(t, m, "fixed", 15), "A", plainTag)
	require.InDelta(t, 113.5, a.GetValues()["pmean__4"], 1e-9, "窗口 4：(112+113+114+115)/4")
	require.InDelta(t, 4, replayParamOf(a, "pmean__4", "window"), 1e-9, "固定成员的参数也如实带在帧里")

	period := mustStepOne(t, m, "fixed", 15, replayWindowParams("pmean__4", 2, nil))
	require.Len(t, period.GetRejectedParams(), 1)
	require.Contains(t, period.GetRejectedParams()[0].GetReason(), "固定成员")
	require.InDelta(t, 113.5, replayRowOf(period, "A", plainTag).GetValues()["pmean__4"], 1e-9, "仍按固定值计算")
}
