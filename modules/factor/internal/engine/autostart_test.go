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
	private := engineTask("ftask_private", "dataset_a", true, "bias")
	private.Set.OwnerInstanceID = "inst-1"
	shared := engineTask("ftask_shared", "dataset_a", true, "bias", "cci")
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{uncompiled(private), uncompiled(shared)}})
	require.NoError(t, err)
	return &Runtime{catalog: cache, runs: newRunTracker()}
}

// 目录里的全部计算任务（共享的与策略实例专属的）随创建自动 init、start，不必手动执行生命周期命令。
func TestAutoStartTaskForEveryTaskInCatalog(t *testing.T) {
	r := autoRuntime(t)
	require.True(t, r.autoStartTask(setRef("ftask_private")))
	require.True(t, r.autoStartTask(setRef("ftask_shared")))
	require.False(t, r.autoStartTask(setRef("ftask_missing")), "不在目录里的任务不自动推进")
	require.False(t, r.autoStartTask(lifecycle.Ref{}), "模块自身不是任务")
	require.False(t, r.autoStartTask(lifecycle.Ref{Kind: "strategy_instance", ID: "ftask_shared"}), "不是因子任务")
}

// completeState 是成员按当前目录算完一期后的状态（定义指纹与参数摘要与目录一致）。
func completeState(member domain.MemberDef) storageio.FactorState {
	compiled, err := domain.CompileDefinition(member.Factor)
	if err != nil {
		panic(err)
	}
	member.Factor = compiled
	return storageio.FactorState{
		Member: member.Alias, FactorID: member.Factor.FactorID, Status: "complete",
		DefinitionHash: domain.DefinitionHash(compiled), ParamsDigest: domain.ParamsDigest(domain.MemberSnapshot(member)),
	}
}

func recordPeriod(r *Runtime, taskID string, completedAt time.Time, mutate func(*pipeline.Outcome)) {
	set, _ := r.catalog.SetByID(taskID)
	outcome := pipeline.Outcome{Status: "complete", RowsWritten: 10}
	for _, member := range set.Members {
		outcome.Factors = append(outcome.Factors, completeState(member))
	}
	if mutate != nil {
		mutate(&outcome)
	}
	r.runs.record(taskID, time.Date(2026, 10, 25, 13, 35, 0, 0, time.UTC), outcome.Status, outcome, completedAt)
}

// 预热到运行的自动确认：只对非专属任务，且要预热之后有一期所有成员都完整算完。
func TestAutoConfirmNeedsAFullPeriodAfterWarmupStarted(t *testing.T) {
	r := autoRuntime(t)
	ref := setRef("ftask_shared")
	warmingSince := time.Date(2026, 10, 25, 13, 36, 0, 0, time.UTC)

	require.False(t, r.autoConfirmTask(ref, warmingSince), "还没有任何一期")

	recordPeriod(r, "ftask_shared", warmingSince.Add(-time.Minute), nil)
	require.False(t, r.autoConfirmTask(ref, warmingSince), "完成时间早于进入预热，不算证据")

	recordPeriod(r, "ftask_shared", warmingSince.Add(time.Minute), nil)
	require.True(t, r.autoConfirmTask(ref, warmingSince))

	require.False(t, r.autoConfirmTask(setRef("ftask_private"), warmingSince), "专属任务不自动确认")
	require.False(t, r.autoConfirmTask(setRef("ftask_missing"), warmingSince))
	require.False(t, r.autoConfirmTask(lifecycle.Ref{}, warmingSince))
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
		"定义指纹对不上": func(o *pipeline.Outcome) { o.Factors[0].DefinitionHash = "sha256:other" },
		"参数摘要对不上": func(o *pipeline.Outcome) { o.Factors[0].ParamsDigest = "sha256:other" },
	}
	since := time.Date(2026, 10, 25, 13, 36, 0, 0, time.UTC)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := autoRuntime(t)
			recordPeriod(r, "ftask_shared", since.Add(time.Minute), mutate)
			require.False(t, r.autoConfirmTask(setRef("ftask_shared"), since))
		})
	}
}

// 同一别名删除重加成不同的固定参数：目录里的成员变了，预热里已经完成的一期不再是证据，要等按新成员算完的一期。
func TestAutoConfirmEvidenceIsBoundToMemberContent(t *testing.T) {
	cache, _ := newTestCache(t)
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 30, default: 5}\noutput: ts_mean(close, window)\n")
	build := func(window float64) domain.EngineTask {
		set := engineTask("ftask_shared", "dataset_a", true)
		set.Members = []domain.MemberDef{{Alias: "pmean", Factor: def, Params: map[string]float64{"window": window}}}
		return uncompiled(set)
	}
	_, err := cache.Apply(CatalogSnapshot{Hash: "h1", Sets: []domain.EngineTask{build(5)}})
	require.NoError(t, err)
	r := &Runtime{catalog: cache, runs: newRunTracker()}
	since := time.Date(2026, 10, 25, 13, 36, 0, 0, time.UTC)
	recordPeriod(r, "ftask_shared", since.Add(time.Minute), nil)
	require.True(t, r.autoConfirmTask(setRef("ftask_shared"), since), "按当前成员算完的一期是证据")

	_, err = cache.Apply(CatalogSnapshot{Hash: "h2", Sets: []domain.EngineTask{build(10)}})
	require.NoError(t, err)
	require.False(t, r.autoConfirmTask(setRef("ftask_shared"), since), "同一别名换了固定参数，旧的一期不是新成员的证据")

	recordPeriod(r, "ftask_shared", since.Add(2*time.Minute), nil)
	require.True(t, r.autoConfirmTask(setRef("ftask_shared"), since), "按新成员算完一期之后才确认")
}
