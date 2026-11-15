package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func fixedSpaces(spaces ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return spaces, nil }
}

func openManagedDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestManagedSpacesFollowEnabledTasksPlansScfAndPauses(t *testing.T) {
	db := openManagedDB(t)
	ctx := context.Background()
	cfg := &Config{}
	spaces, err := managedSpaces(ctx, cfg, db)
	require.NoError(t, err)
	require.Empty(t, spaces, "没有任务、计划和暂停时不管理任何空间")

	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "t1", TaskName: "t1", DataType: "kline", Enabled: true}))
	spaces, err = managedSpaces(ctx, cfg, db)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto"}, spaces, "不配云函数时，启用任务的空间也要管理")

	require.NoError(t, db.FetchPlans().SetPaused(ctx, "stockhk", true))
	cfg.SCFMarkets = map[string]map[string][]string{"stockcn": {"equity": {"ap-nanjing"}}}
	spaces, err = managedSpaces(ctx, cfg, db)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto", "stockcn", "stockhk"}, spaces)
}

// 重置清空任务和计划表、保留暂停表：第一次 init 要能解除暂停，否则采集再也不恢复。
func TestAfterResetClearsPauseWhenNoTasksOrPlansRemain(t *testing.T) {
	db := openManagedDB(t)
	ctx := context.Background()
	cfg := &Config{}
	require.NoError(t, db.FetchPlans().SetSystemPaused(ctx, "crypto", true))
	control := &collectorControl{plans: db.FetchPlans(), spaces: func(ctx context.Context) ([]string, error) { return managedSpaces(ctx, cfg, db) }}
	control.afterReset(ctx)
	paused, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.Empty(t, paused)
}

// 运行中新增的空间也要被暂停。
func TestPauseCoversSpacesAddedWhileRunning(t *testing.T) {
	db := openManagedDB(t)
	ctx := context.Background()
	cfg := &Config{}
	control := &collectorControl{plans: db.FetchPlans(), spaces: func(ctx context.Context) ([]string, error) { return managedSpaces(ctx, cfg, db) }}
	require.Empty(t, control.managed(ctx))
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "t1", TaskName: "t1", DataType: "kline", Enabled: true}))
	require.NoError(t, control.setPaused(ctx, true))
	paused, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.True(t, paused["crypto"])
}
