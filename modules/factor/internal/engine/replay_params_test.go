package engine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 闭环回放（因子动态调参执行计划 2.9）：每期请求带来的参数快照真正改变计算；没带的期次按“请求优先、冻结记录、默认值”回退；
// 因子帧里带上这一期各标的实际用的参数值（<别名>__<参数名>），按标的覆盖逐标的体现。
func TestReplayStepAppliesRequestedParams(t *testing.T) {
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 6, default: 2}\noutputs:\n  m: ts_mean(close, window)\n")
	set := uncompiled(domain.EngineSet{
		Set: domain.FactorSet{SetID: "fset_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m", SubjectMode: domain.SubjectModeAll,
			ResultDatasetID: "result_dataset_a", Status: domain.SetStatusEnabled, OwnerInstanceID: "inst"},
		Members: []domain.MemberDef{{Alias: "fast", Factor: def}}, ResultReady: true,
	})
	m, _ := newReplayManagerWithSet(t, set)
	ctx := context.Background()
	_, _, err := m.Open(ctx, ReplayOpenRequest{ReplayID: "tuned", SetID: "fset_replay", From: rt0.Add(14 * time.Minute).Format(time.RFC3339), To: rt0.Add(17 * time.Minute).Format(time.RFC3339)})
	require.NoError(t, err)
	at := rt0.Add(15 * time.Minute).Format(time.RFC3339)

	// 默认窗口 2：close 在第 15 分钟是 115，均值 (114+115)/2。
	step, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at})
	require.NoError(t, err)
	a := step.Subjects["A"].Factors["fast"]
	require.InDelta(t, 114.5, a["fast_m"], 1e-9)
	require.InDelta(t, 2, a["fast__window"], 1e-9, "没有请求的期次实际用的是默认值")
	require.Equal(t, domain.ParamsDefault, step.States["fast"].ParamsSource)

	// 请求带来的快照：A 用窗口 4、B 用窗口 2（覆盖按标的）。
	step, _, err = m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at,
		Params: map[string]domain.ParamSnapshot{"fast": {"window": {Default: 2, Overrides: map[string]float64{"A": 4}}}}})
	require.NoError(t, err)
	require.InDelta(t, 113.5, step.Subjects["A"].Factors["fast"]["fast_m"], 1e-9, "A 的窗口是 4：(112+113+114+115)/4")
	require.InDelta(t, 4, step.Subjects["A"].Factors["fast"]["fast__window"], 1e-9)
	require.InDelta(t, 114.5, step.Subjects["B"].Factors["fast"]["fast_m"], 1e-9)
	require.InDelta(t, 2, step.Subjects["B"].Factors["fast"]["fast__window"], 1e-9)
	require.Equal(t, domain.ParamsRequested, step.States["fast"].ParamsSource)

	// 不合法的快照（超出范围、没有的成员）被拒绝。
	_, status, err := m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at, Params: map[string]domain.ParamSnapshot{"fast": {"window": {Default: 99}}}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	_, status, err = m.Step(ctx, ReplayStepRequest{ReplayID: "tuned", Period: at, Params: map[string]domain.ParamSnapshot{"nope": {"window": {Default: 3}}}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, status)
}
