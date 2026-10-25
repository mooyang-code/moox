package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

func autoRuntime(t *testing.T) *Runtime {
	t.Helper()
	cache, _ := newTestCache(t)
	private := engineSet("fset_private", "dataset_a", true, "bias")
	private.Set.OwnerInstanceID = "inst-1"
	shared := engineSet("fset_shared", "dataset_a", true, "bias", "cci")
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineSet{uncompiled(private), uncompiled(shared)}})
	require.NoError(t, err)
	return &Runtime{catalog: cache, runs: newRunTracker()}
}

// 目录里的全部计算任务（共享的与策略实例专属的）随创建自动 init、start，不必手动执行生命周期命令。
func TestAutoStartSetForEverySetInCatalog(t *testing.T) {
	r := autoRuntime(t)
	require.True(t, r.autoStartSet(setRef("fset_private")))
	require.True(t, r.autoStartSet(setRef("fset_shared")))
	require.False(t, r.autoStartSet(setRef("fset_missing")), "不在目录里的集合不自动推进")
	require.False(t, r.autoStartSet(lifecycle.Ref{}), "模块自身不是集合")
	require.False(t, r.autoStartSet(lifecycle.Ref{Kind: "strategy_instance", ID: "fset_shared"}), "不是因子集")
}

func recordPeriod(r *Runtime, setID string, completedAt time.Time, mutate func(*pipeline.Outcome)) {
	outcome := pipeline.Outcome{
		Status: "complete", RowsWritten: 10,
		Factors: []storageio.FactorState{{Member: "bias", Status: "complete"}, {Member: "cci", Status: "complete"}},
	}
	if mutate != nil {
		mutate(&outcome)
	}
	r.runs.record(setID, time.Date(2026, 10, 25, 13, 35, 0, 0, time.UTC), outcome.Status, outcome, completedAt)
}

// 预热到运行的自动确认：只对非专属任务，且要预热之后有一期所有成员都完整算完。
func TestAutoConfirmNeedsAFullPeriodAfterWarmupStarted(t *testing.T) {
	r := autoRuntime(t)
	ref := setRef("fset_shared")
	warmingSince := time.Date(2026, 10, 25, 13, 36, 0, 0, time.UTC)

	require.False(t, r.autoConfirmSet(ref, warmingSince), "还没有任何一期")

	recordPeriod(r, "fset_shared", warmingSince.Add(-time.Minute), nil)
	require.False(t, r.autoConfirmSet(ref, warmingSince), "完成时间早于进入预热，不算证据")

	recordPeriod(r, "fset_shared", warmingSince.Add(time.Minute), nil)
	require.True(t, r.autoConfirmSet(ref, warmingSince))

	require.False(t, r.autoConfirmSet(setRef("fset_private"), warmingSince), "专属任务不自动确认")
	require.False(t, r.autoConfirmSet(setRef("fset_missing"), warmingSince))
	require.False(t, r.autoConfirmSet(lifecycle.Ref{}, warmingSince))
}

func TestAutoConfirmRejectsIncompletePeriods(t *testing.T) {
	cases := map[string]func(*pipeline.Outcome){
		"有失败标的":   func(o *pipeline.Outcome) { o.FailedSubjects = []string{"BTC"} },
		"有预热中的标的": func(o *pipeline.Outcome) { o.Warming = 3 },
		"没有写入行":   func(o *pipeline.Outcome) { o.RowsWritten = 0 },
		"期次失败":    func(o *pipeline.Outcome) { o.Status = "failed" },
		"有成员被跳过":  func(o *pipeline.Outcome) { o.Factors[1].Status = "skipped" },
		"成员有失败标的": func(o *pipeline.Outcome) { o.Factors[0].FailedSubjects = []string{"ETH"} },
		"成员清单缺一个": func(o *pipeline.Outcome) { o.Factors = o.Factors[:1] },
		"成员清单多一个": func(o *pipeline.Outcome) {
			o.Factors = append(o.Factors, storageio.FactorState{Member: "extra", Status: "complete"})
		},
	}
	since := time.Date(2026, 10, 25, 13, 36, 0, 0, time.UTC)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := autoRuntime(t)
			recordPeriod(r, "fset_shared", since.Add(time.Minute), mutate)
			require.False(t, r.autoConfirmSet(setRef("fset_shared"), since))
		})
	}
}
