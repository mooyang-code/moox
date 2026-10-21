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

type preloadDataFake struct{ prepared []string }

func (f *preloadDataFake) Prepare(_ context.Context, set domain.EngineSet, _ []string) error {
	f.prepared = append(f.prepared, set.Set.SetID)
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
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineSet{uncompiled(engineSet("fset_a", "dataset_a", true, "bias"))}})
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

// 集合要先 init（预载窗口）才就绪；只有预热或运行才通过门禁；预载没完成时检查不过。
func TestEngineLifecycleGatesSetUntilStarted(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t)
	ref := setRef("fset_a")

	r.life.Refresh(ctx)
	require.Equal(t, lifecycle.StageUninitialized, r.life.Stage(ref))
	require.False(t, r.gate("fset_a"))

	require.False(t, lifecycle.Passed(r.checkSet(ctx, ref)), "窗口没有预载时检查不过")

	obj, err := r.life.Command(ctx, lifecycle.CmdInit, ref)
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageReady, obj.Stage)
	require.Equal(t, []string{"fset_a"}, fake.prepared)
	require.True(t, r.preload.Ready("fset_a"))
	require.False(t, r.gate("fset_a"), "就绪但没有启动：不计算")

	_, err = r.life.Command(ctx, lifecycle.CmdStart, ref)
	require.NoError(t, err)
	require.True(t, r.gate("fset_a"), "预热中允许计算")
	_, err = r.life.Command(ctx, lifecycle.CmdConfirm, ref)
	require.NoError(t, err)
	require.True(t, r.gate("fset_a"))

	_, err = r.life.Command(ctx, lifecycle.CmdPause, ref)
	require.NoError(t, err)
	require.False(t, r.gate("fset_a"), "暂停后不计算")
}

// 已预载且回看够长的集合再次 init 不重复预载。
func TestPrepareSetSkipsWhenAlreadyPrepared(t *testing.T) {
	r, fake := newLifecycleRuntime(t)
	require.NoError(t, r.preload.PrepareSet(context.Background(), "fset_a"))
	require.NoError(t, r.preload.PrepareSet(context.Background(), "fset_a"))
	require.Len(t, fake.prepared, 1)
	require.Error(t, r.preload.PrepareSet(context.Background(), "missing"))
}
