package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

func autoRuntime(t *testing.T) *Runtime {
	t.Helper()
	cache, _ := newTestCache(t)
	private := engineTask("ftask_private", "dataset_a", true, "bias")
	private.Set.BoundInstanceID = "inst-1"
	shared := engineTask("ftask_shared", "dataset_a", true, "bias", "cci")
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{uncompiled(private), uncompiled(shared)}})
	require.NoError(t, err)
	return &Runtime{catalog: cache, runs: newRunTracker()}
}

// 目录里的全部计算任务（已关联策略实例的与没有关联的）随创建自动 init、start，不必手动执行生命周期命令。
func TestAutoStartTaskForEveryTaskInCatalog(t *testing.T) {
	r := autoRuntime(t)
	require.True(t, r.autoStartTask(setRef("ftask_private")))
	require.True(t, r.autoStartTask(setRef("ftask_shared")))
	require.False(t, r.autoStartTask(setRef("ftask_missing")), "不在目录里的任务不自动推进")
	require.False(t, r.autoStartTask(lifecycle.Ref{}), "模块自身不是任务")
	require.False(t, r.autoStartTask(lifecycle.Ref{Kind: "strategy_instance", ID: "ftask_shared"}), "不是因子任务")
}

// 没有启用成员的任务不自动启动；成员启用、目录同步之后重新判断就会启动。
func TestAutoStartSkipsTasksWithoutMembers(t *testing.T) {
	cache, _ := newTestCache(t)
	empty := engineTask("ftask_empty", "dataset_a", true)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h1", Sets: []domain.EngineTask{uncompiled(empty)}})
	require.NoError(t, err)
	r := &Runtime{catalog: cache, runs: newRunTracker()}
	require.False(t, r.autoStartTask(setRef("ftask_empty")))

	withMember := engineTask("ftask_empty", "dataset_a", true, "bias")
	_, err = cache.Apply(CatalogSnapshot{Hash: "h2", Sets: []domain.EngineTask{uncompiled(withMember)}})
	require.NoError(t, err)
	require.True(t, r.autoStartTask(setRef("ftask_empty")))
}

// 试运行中的计算任务直接认可进入运行；模块自身和别的对象类型不认可。
func TestAutoConfirmAcceptsFactorTasks(t *testing.T) {
	r := autoRuntime(t)
	require.True(t, r.autoConfirmTask(setRef("ftask_shared"), time.Time{}))
	require.False(t, r.autoConfirmTask(lifecycle.Ref{}, time.Time{}))
}
