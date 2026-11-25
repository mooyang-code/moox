package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
)

func seedingTask(id string, seeding bool) domain.EngineTask {
	task := engineTask(id, "dataset_a", true, "bias")
	task.SourceSeeding = seeding
	return task
}

// 源数据集在历史补齐中：任务照常预载，但检查项“源数据历史补齐”不通过、不计算；补齐结束重新预载后通过、开始计算。
func TestSourceSeedingTaskIsGatedUntilReleased(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t, seedingTask("ftask_a", true))
	r.preload.once(ctx)
	require.Equal(t, []string{"ftask_a"}, fake.prepared, "补齐中照常预载窗口")
	r.refreshTaskChecks(ctx)

	checks, _ := r.checks.get("ftask_a")
	require.Equal(t, domain.TaskCheckFail, checkStatus(checks, seedingCheckName))
	require.Equal(t, domain.TaskCheckPass, checkStatus(checks, windowCheckName))
	for _, check := range checks {
		if check.Name == seedingCheckName {
			require.Equal(t, "历史补齐中，进度看 collector status", check.Message)
		}
	}
	require.False(t, r.gate("ftask_a"), "补齐中不计算")
	require.True(t, failsBeyondWindow(checks), "补齐中的任务不参与恢复追赶")

	// 补齐状态仍为真时不会重复预载。
	r.preload.once(ctx)
	require.Len(t, fake.prepared, 1)

	// 补齐结束：目录里 SourceSeeding 变假，预载循环重新预载，完成后立即重查通过。
	_, err := r.catalog.Apply(CatalogSnapshot{Hash: "h2", Sets: []domain.EngineTask{uncompiled(seedingTask("ftask_a", false))}})
	require.NoError(t, err)
	r.preload.once(ctx)
	require.Equal(t, []string{"ftask_a", "ftask_a"}, fake.prepared, "翻转后重新预载")
	checks, _ = r.checks.get("ftask_a")
	require.Equal(t, domain.TaskCheckPass, checkStatus(checks, seedingCheckName))
	require.True(t, r.gate("ftask_a"), "放行后开始计算")
}

// 没有补齐的任务检查项通过；补齐状态由假变真不触发重新预载。
func TestSourceSeedingStartingDoesNotRepreload(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t, seedingTask("ftask_a", false))
	r.preload.once(ctx)
	r.refreshTaskChecks(ctx)
	checks, _ := r.checks.get("ftask_a")
	require.Equal(t, domain.TaskCheckPass, checkStatus(checks, seedingCheckName))
	require.True(t, r.gate("ftask_a"))

	_, err := r.catalog.Apply(CatalogSnapshot{Hash: "h2", Sets: []domain.EngineTask{uncompiled(seedingTask("ftask_a", true))}})
	require.NoError(t, err)
	r.preload.once(ctx)
	require.Len(t, fake.prepared, 1)
	r.refreshTaskChecks(ctx)
	require.False(t, r.gate("ftask_a"), "补齐开始后下一轮检查起不计算")
}

type failingOnceData struct {
	preloadDataFake
	failures int
}

func (f *failingOnceData) Prepare(ctx context.Context, set domain.EngineTask, subjects []string) error {
	if f.failures > 0 {
		f.failures--
		return errors.New("读 Storage 失败")
	}
	return f.preloadDataFake.Prepare(ctx, set, subjects)
}

// 放行后的重新预载成功才清待办：失败重试期间不清，成功时只清一次、并且在任务标记就绪之前。
func TestSeedReleaseClearsPendingAfterSuccessfulReprepare(t *testing.T) {
	ctx := context.Background()
	catalog := &mutableSets{sets: map[string]domain.EngineTask{"s1": seedingTask("s1", true)}}
	data := &failingOnceData{}
	p := newPreloader(catalog, includeSubjects{}, data, nil, nil)
	var cleared []domain.FactorTask
	p.onSeedReleased = func(set domain.FactorTask) {
		require.False(t, p.Ready(set.TaskID), "清待办发生在任务重新标记就绪之前")
		cleared = append(cleared, set)
	}
	p.once(ctx)
	require.True(t, p.Ready("s1"))
	require.Empty(t, cleared, "补齐期间的预载不清待办")

	catalog.sets["s1"] = seedingTask("s1", false)
	data.failures = 1
	p.once(ctx)
	require.False(t, p.Ready("s1"))
	require.Empty(t, cleared, "重新预载失败不清待办")

	p.once(ctx)
	require.True(t, p.Ready("s1"))
	require.Len(t, cleared, 1, "失败重试成功后仍然清一次")
	require.Equal(t, "dataset_a", cleared[0].SourceDatasetID)

	p.once(ctx)
	require.Len(t, cleared, 1)
}

// clearSeedPending 只清数据源上的 earliest_moved 待办。
func TestClearSeedPendingRemovesEarliestMovedOnly(t *testing.T) {
	w, store := newTestWriter(t)
	post(t, w, wireRequest(10, 11))
	post(t, w, wireRequest(8, 9))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	pending, err := store.Pending(src)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, seriesstore.PendingEarliestMoved, pending[0].Reason)
	require.Equal(t, pending[0].From, pending[0].To, "待办只记旧的最早一根")

	r := &Runtime{data: localdata.New(localdata.Config{Store: store})}
	r.clearSeedPending(domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "spot", Freq: "1m"})
	pending, err = store.Pending(src)
	require.NoError(t, err)
	require.Empty(t, pending)
}
