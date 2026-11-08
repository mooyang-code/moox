package verify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func healthy() Input {
	return Input{
		Now: now, HasPlan: true, NeedsStream: true, StreamOnline: true, FactorDeployed: true,
		Datasets:  []Dataset{{ID: "d", Registered: true, Frequency: "1m", EffectiveFrom: now.Add(-time.Hour), Expected: 100}},
		Factors:   []Factor{{TaskID: "f", Frequency: "1m", Baseline: 1, LastPeriod: 2, LastStatus: "degraded", SourceEnabled: true}},
		Instances: []Instance{{ID: "i", Enabled: true, SessionID: "s"}},
	}
}

func status(checks []Check, name string) string {
	for _, check := range checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

func TestHealthyInstallPasses(t *testing.T) {
	checks := Evaluate(healthy())
	require.True(t, Settled(checks))
	for _, check := range checks {
		require.Equal(t, Pass, check.Status, check.Name)
	}
}

func TestPlanWaitsWithReasonAndCloudOnlySpaceNeedsNoStream(t *testing.T) {
	in := healthy()
	in.HasPlan, in.PlanIssue = false, "等 collector-stream 上线"
	check := Evaluate(in)[0]
	require.Equal(t, Wait, check.Status)
	require.Contains(t, check.Detail, "collector-stream")

	in = healthy()
	in.NeedsStream, in.StreamOnline = false, false
	require.Equal(t, Pass, status(Evaluate(in), "plan"))
}

func TestKlineExpectedZero(t *testing.T) {
	in := healthy()
	in.Datasets[0].Registered = false
	require.Equal(t, Wait, status(Evaluate(in), "kline"), "没进计划时不能算通过")

	in = healthy()
	in.Datasets[0].Expected, in.Datasets[0].EffectiveFrom = 0, now.Add(time.Minute)
	require.Equal(t, Wait, status(Evaluate(in), "kline"), "第一根还没收盘时不能算通过")

	in = healthy()
	in.Datasets[0].Expected = 0
	require.Equal(t, Skip, status(Evaluate(in), "kline"), "超过等待时间仍是 0，按休市处理")

	in = healthy()
	in.Datasets[0].Expected, in.Datasets[0].Frequency, in.Datasets[0].EffectiveFrom = 0, "1h", now
	require.Equal(t, Skip, status(Evaluate(in), "kline"))
}

func TestKlineToleratesSmallGaps(t *testing.T) {
	in := healthy()
	in.Datasets[0].Missing = 5
	require.Equal(t, Pass, status(Evaluate(in), "kline"))
	in.Datasets[0].Missing = 6
	require.Equal(t, Wait, status(Evaluate(in), "kline"))
}

func TestFactorRules(t *testing.T) {
	in := healthy()
	in.Factors[0].LastPeriod = 1
	require.Equal(t, Wait, status(Evaluate(in), "factor"))

	in = healthy()
	in.Factors[0].LastStatus = "failed"
	require.Equal(t, Wait, status(Evaluate(in), "factor"))

	in = healthy()
	in.Factors[0].Frequency, in.Factors[0].LastPeriod = "1h", 1
	require.Equal(t, Pass, status(Evaluate(in), "factor"), "频率大于 1m 的不要求前进")

	in = healthy()
	in.Factors[0].SourceEnabled = false
	require.Equal(t, Skip, status(Evaluate(in), "factor"))

	in = healthy()
	in.FactorDeployed = false
	require.Equal(t, Skip, status(Evaluate(in), "factor"))
}

func TestStrategyRules(t *testing.T) {
	in := healthy()
	in.Instances[0].SessionID = ""
	require.Equal(t, Wait, status(Evaluate(in), "strategy"))

	in = healthy()
	in.Instances[0].Enabled = false
	require.Equal(t, Skip, status(Evaluate(in), "strategy"))
}

// 引用的因子任务已不存在的停用实例只在说明里标出，不改变检查结果。
func TestStrategyMarksIncompleteDeletion(t *testing.T) {
	in := healthy()
	in.Instances = append(in.Instances, Instance{ID: "gone", MissingFactorTasks: []string{"ftask_x"}})
	var check Check
	for _, item := range Evaluate(in) {
		if item.Name == "strategy" {
			check = item
		}
	}
	require.Equal(t, Pass, check.Status)
	require.Contains(t, check.Detail, "gone 引用的因子任务已不存在")

	in.Instances = in.Instances[1:]
	for _, item := range Evaluate(in) {
		if item.Name == "strategy" {
			require.Equal(t, Skip, item.Status)
			require.Contains(t, item.Detail, "删除实例即可清理")
		}
	}
}

func TestNoEnabledTasksFails(t *testing.T) {
	checks := Evaluate(Input{Now: now})
	require.Equal(t, Fail, checks[0].Status)
	require.Equal(t, CodeNoCollectionTasks, checks[0].Code)
	require.True(t, Settled(checks), "空间没有任务不用再等")
}

func TestDisabledTasksFailAndFailDoesNotWait(t *testing.T) {
	checks := Evaluate(Input{Now: now, TasksDisabled: true})
	require.Equal(t, Fail, checks[0].Status, "任务默认停用，全停用就是还没 setup start，不能假绿")
	require.Equal(t, CodeCollectionNotStarted, checks[0].Code)
	in := Input{Now: now, Instances: []Instance{{ID: "s", Enabled: true}}}
	require.True(t, Settled(Evaluate(in)), "已有失败项时不用等别的检查")
}

func TestSmallDatasetGapIsNotDiluted(t *testing.T) {
	in := Input{Now: now, HasPlan: true, Datasets: []Dataset{
		{ID: "large", Registered: true, Frequency: "1m", Expected: 1000},
		{ID: "small", Registered: true, Frequency: "1m", Expected: 10, Missing: 10},
	}}
	check := klines(in)
	require.Equal(t, Wait, check.Status)
	require.Contains(t, check.Detail, "small")
}

func TestMarketClosedFactorOnlyMustNotFail(t *testing.T) {
	in := Input{Now: now.Add(time.Hour), HasPlan: true, FactorDeployed: true,
		Datasets: []Dataset{{ID: "d", Registered: true, Frequency: "1m", EffectiveFrom: now}},
		Factors:  []Factor{{TaskID: "f", Frequency: "1m", LastPeriod: 5, Baseline: 5, SourceEnabled: true}}}
	checks := Evaluate(in)
	require.Equal(t, Skip, checks[1].Status)
	require.Equal(t, Pass, checks[2].Status)
	in.Factors[0].LastStatus = "failed"
	require.Equal(t, Wait, Evaluate(in)[2].Status)
}

func TestFinishTurnsWaitIntoFail(t *testing.T) {
	in := healthy()
	in.HasPlan = false
	checks := Finish(Evaluate(in))
	require.Equal(t, Fail, checks[0].Status)
	require.Equal(t, CodeCollectionNotStarted, checks[0].Code)
}
