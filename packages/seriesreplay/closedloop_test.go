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
	_, err := r.Run(context.Background(), spec(6))
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

// 中断后续跑：生效参数随周期记录保存，续跑从上一期记录恢复，不丢参数；写回因子结果时用与求值同一份参数。
func TestClosedLoopResumeRestoresEffectiveParams(t *testing.T) {
	r, f, s, tr := newRunner(t)
	s.next = func(period time.Time) MemberParams { return window(float64(10 + period.Minute())) }
	tr.failNext = errors.New("trade 暂时不可达")
	// 第 0 期 Trade 推进失败，第 0 期记录（含生效参数）已落库。
	_, err := r.Run(context.Background(), spec(3))
	require.Error(t, err)
	_, err = r.Run(context.Background(), spec(3))
	require.NoError(t, err)
	at := func(minute int) string { return t0.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339) }
	require.Equal(t, window(10), f.params[at(1)], "续跑后第 1 期仍带着第 0 期产出的参数")
	require.Equal(t, window(10), f.params[at(2)])
	require.Len(t, f.writes, 3)
}

func TestFactorDriftMatchesByMemberAlias(t *testing.T) {
	set := []FactorInfo{{Member: "fast", FactorID: "bias", DefinitionHash: "h1"}, {Member: "slow", FactorID: "bias", DefinitionHash: "h1"}}
	require.Empty(t, factorDrift(map[string]string{"fast": "h1", "slow": "h1"}, set))
	require.Contains(t, factorDrift(map[string]string{"mid": "h1"}, set), "mid")
	require.Contains(t, factorDrift(map[string]string{"fast": "h2"}, set), "指纹")
}

// 实例声明了 factors 时因子集合就是实例的私有因子集：省略 set_id 取它的，写了别的被拒绝。
func TestRunUsesInstancePrivateFactorSet(t *testing.T) {
	r, _, s, _ := newRunner(t)
	s.setID = "fset_i1"
	omitted := spec(2)
	omitted.SetID = ""
	run, err := r.Run(context.Background(), omitted)
	require.NoError(t, err)
	require.Equal(t, "fset_i1", run.Spec.SetID)

	r2, _, s2, _ := newRunner(t)
	s2.setID = "fset_i1"
	_, err = r2.Run(context.Background(), spec(2))
	require.ErrorContains(t, err, "私有因子集")

	r3, _, _, _ := newRunner(t)
	missing := spec(2)
	missing.SetID = ""
	_, err = r3.Run(context.Background(), missing)
	require.ErrorContains(t, err, "set_id")
}
