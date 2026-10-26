package seriesreplay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func window(value float64) MemberParams {
	return MemberParams{"f": {"window": {Default: value}}}
}

// 闭环回放：策略每期产出下一期的参数，下一期的因子步进带着它；策略没有产出的期次沿用上一期的生效参数（缺值沿用）；
// 第一期没有上一期，参数交给因子引擎（冻结记录或默认值）。
func TestClosedLoopCarriesParamsIntoNextStep(t *testing.T) {
	r, f, s, _ := newRunner(t)
	// ok 的周期是偶数分钟（0、2、4）；第 0 期产出窗口 10，第 2 期产出窗口 30，第 4 期不产出。
	s.next = func(period time.Time) MemberParams {
		switch period.Minute() {
		case 0:
			return window(10)
		case 2:
			return window(30)
		}
		return nil
	}
	_, err := r.Run(context.Background(), noWrite(spec(6)))
	require.NoError(t, err)
	at := func(minute int) string { return t0.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339) }
	require.Nil(t, f.params[at(0)], "第一期交给因子引擎")
	require.Equal(t, window(10), f.params[at(1)])
	require.Equal(t, window(10), f.params[at(2)], "第 1 期被跳过没有产出，沿用")
	require.Equal(t, window(30), f.params[at(3)])
	require.Equal(t, window(30), f.params[at(4)])
	require.Equal(t, window(30), f.params[at(5)], "第 4 期没有产出，沿用")
	rec, err := r.Store.GetRecord("r1", t0.Add(2*time.Minute).Unix())
	require.NoError(t, err)
	require.Equal(t, window(30), rec.Decision.NextParams)
	require.Equal(t, window(30), rec.Decision.EffectiveParams)
}

// 中断后续跑：生效参数随周期记录保存，续跑从上一期记录恢复，不丢参数。
func TestClosedLoopResumeRestoresEffectiveParams(t *testing.T) {
	r, f, s, tr := newRunner(t)
	s.next = func(period time.Time) MemberParams { return window(float64(10 + period.Minute())) }
	tr.failNext = errors.New("trade 暂时不可达")
	// 第 0 期 Trade 推进失败，第 0 期记录（含生效参数）已落库。
	_, err := r.Run(context.Background(), noWrite(spec(3)))
	require.Error(t, err)
	_, err = r.Run(context.Background(), noWrite(spec(3)))
	require.NoError(t, err)
	at := func(minute int) string { return t0.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339) }
	require.Equal(t, window(10), f.params[at(1)], "续跑后第 1 期仍带着第 0 期产出的参数")
	require.Equal(t, window(10), f.params[at(2)])
}

// noWrite 去掉写回因子结果：调参策略的闭环回放不允许写回（见 TestTunedReplayRejectsWriteFactors）。
func noWrite(s Spec) Spec {
	s.WriteFactors = false
	return s
}

// 调参策略的闭环回放用的是回放中的参数，写回会让正式结果与冻结记录对不上：拒绝。
func TestTunedReplayRejectsWriteFactors(t *testing.T) {
	r, f, s, _ := newRunner(t)
	s.next = func(time.Time) MemberParams { return window(10) }
	_, err := r.Run(context.Background(), spec(2))
	require.ErrorContains(t, err, "write_factors")
	require.Empty(t, f.writes)
}

func TestFactorDriftMatchesByMemberKey(t *testing.T) {
	set := []FactorInfo{{Member: "fast", FactorID: "bias", DefinitionHash: "h1"}, {Member: "slow", FactorID: "bias", DefinitionHash: "h1"}}
	require.Empty(t, factorDrift(map[string]string{"fast": "h1", "slow": "h1"}, set))
	require.Contains(t, factorDrift(map[string]string{"mid": "h1"}, set), "mid")
	require.Contains(t, factorDrift(map[string]string{"fast": "h2"}, set), "指纹")
}

// 因子任务就是实例关联的任务：省略 task_id 取它的，写了别的被拒绝；实例没有关联任务时拒绝。
func TestRunUsesInstanceFactorTask(t *testing.T) {
	r, _, s, _ := newRunner(t)
	s.taskID = "ftask_other"
	omitted := noWrite(spec(2))
	omitted.TaskID = ""
	run, err := r.Run(context.Background(), omitted)
	require.NoError(t, err)
	require.Equal(t, "ftask_other", run.Spec.TaskID)

	r2, _, s2, _ := newRunner(t)
	s2.taskID = "ftask_other"
	_, err = r2.Run(context.Background(), spec(2))
	require.ErrorContains(t, err, "关联的因子任务是 ftask_other")
}

// 策略产出了引擎拒绝的参数：记录里保存的生效参数以引擎实际用的为基础，非法快照不会留下来，续跑也不会反复用到它；
// 之后策略产出合法的参数照常生效。
func TestClosedLoopDropsParamsRejectedByFactorEngine(t *testing.T) {
	r, f, s, _ := newRunner(t)
	bad := window(999)
	s.next = func(period time.Time) MemberParams {
		switch period.Minute() {
		case 0:
			return bad // 第 1 期会被引擎拒绝
		case 2:
			return window(30)
		}
		return nil
	}
	f.applied = func(req FactorStepRequest) (MemberParams, []ParamRejection) {
		if got := req.Params["f"]["window"].Default; got == 999 {
			return nil, []ParamRejection{{Member: "f", Reason: "参数 window 超出范围"}}
		}
		return req.Params, nil
	}
	_, err := r.Run(context.Background(), noWrite(spec(5)))
	require.NoError(t, err)
	rec, err := r.Store.GetRecord("r1", t0.Add(2*time.Minute).Unix())
	require.NoError(t, err)
	require.Equal(t, window(30), rec.Decision.EffectiveParams)
	rec, err = r.Store.GetRecord("r1", t0.Add(time.Minute).Unix())
	require.NoError(t, err)
	require.NotContains(t, rec.Decision.EffectiveParams, "f", "被拒绝的非法快照不留在记录里")
}
