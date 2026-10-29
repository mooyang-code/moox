package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type preloadDataFake struct {
	prepared []string
	released []domain.FactorTask
	// hold 不为空时 Prepare 等它关闭才返回，用来让恢复追赶的本地同步停在“同步中”。
	hold chan struct{}
}

func (f *preloadDataFake) ReleaseTask(set domain.FactorTask) { f.released = append(f.released, set) }

func (f *preloadDataFake) Prepare(ctx context.Context, set domain.EngineTask, _ []string) error {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.prepared = append(f.prepared, set.Set.TaskID)
	return nil
}

type lifecycleSourceFake struct{}

func (lifecycleSourceFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (lifecycleSourceFake) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTCUSDT"}, nil
}

func newLifecycleRuntime(t *testing.T) (*Runtime, *preloadDataFake) {
	t.Helper()
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{uncompiled(engineTask("ftask_a", "dataset_a", true, "bias"))}})
	require.NoError(t, err)
	fake := new(preloadDataFake)
	r := &Runtime{
		cfg:     &Config{Lifecycle: LifecycleConfig{StateFile: filepath.Join(t.TempDir(), "lifecycle.json")}},
		catalog: cache, lease: &leaseState{}, data: localdata.New(localdata.Config{}),
	}
	r.preload = newPreloader(cache, lifecycleSourceFake{}, fake, r.active, nil)
	r.life, err = r.newLifecycle()
	require.NoError(t, err)
	return r, fake
}

// 任务要先 init（预载窗口）才就绪；只有预热或运行才通过门禁；预载没完成时检查不过。
func TestEngineLifecycleGatesTaskUntilStarted(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t)
	ref := setRef("ftask_a")

	r.life.Refresh(ctx)
	require.Equal(t, lifecycle.StageUninitialized, r.life.Stage(ref))
	require.False(t, r.gate("ftask_a"))

	require.False(t, lifecycle.Passed(r.checkTask(ctx, ref)), "窗口没有预载时检查不过")

	obj, err := r.life.Command(ctx, lifecycle.CmdInit, ref)
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageReady, obj.Stage)
	require.Equal(t, []string{"ftask_a"}, fake.prepared)
	require.True(t, r.preload.Ready("ftask_a"))
	require.False(t, r.gate("ftask_a"), "就绪但没有启动：不计算")

	_, err = r.life.Command(ctx, lifecycle.CmdStart, ref)
	require.NoError(t, err)
	require.True(t, r.gate("ftask_a"), "预热中允许计算")
	_, err = r.life.Command(ctx, lifecycle.CmdConfirm, ref)
	require.NoError(t, err)
	require.True(t, r.gate("ftask_a"))

	_, err = r.life.Command(ctx, lifecycle.CmdPause, ref)
	require.NoError(t, err)
	require.False(t, r.gate("ftask_a"), "暂停后不计算")
}

// 已预载且回看够长的任务再次 init 不重复预载。
func TestPrepareSetSkipsWhenAlreadyPrepared(t *testing.T) {
	r, fake := newLifecycleRuntime(t)
	require.NoError(t, r.preload.PrepareSet(context.Background(), "ftask_a"))
	require.NoError(t, r.preload.PrepareSet(context.Background(), "ftask_a"))
	require.Len(t, fake.prepared, 1)
	require.Error(t, r.preload.PrepareSet(context.Background(), "missing"))
}

// 心跳里的任务暂停标记：手动暂停的任务标为暂停，运行中的不标；没有生命周期控制器时都不标。
func TestMarkPausedRunnersFollowsTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	r, _ := newLifecycleRuntime(t)
	ref := setRef("ftask_a")
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		_, err := r.life.Command(ctx, cmd, ref)
		require.NoError(t, err)
	}
	runners := []domain.RunnerStatus{{TaskID: "ftask_a"}}
	markPausedRunners(runners, r.life)
	require.False(t, runners[0].Paused, "运行中的任务不是暂停")

	_, err := r.life.Command(ctx, lifecycle.CmdPause, ref)
	require.NoError(t, err)
	markPausedRunners(runners, r.life)
	require.True(t, runners[0].Paused)

	_, err = r.life.Command(ctx, lifecycle.CmdResume, ref)
	require.NoError(t, err)
	markPausedRunners(runners, r.life)
	require.True(t, runners[0].Paused, "恢复追赶中、放行前仍算有意停下，Monitor 不对它做新鲜度检查")

	other := []domain.RunnerStatus{{TaskID: "ftask_a", Paused: true}}
	markPausedRunners(other, nil)
	require.True(t, other[0].Paused, "没有控制器时不改动")
}
