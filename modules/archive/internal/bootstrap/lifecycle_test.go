package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// newLifecycleTestEnv 打开一个空的归档管理库并建好生命周期控制器。
func newLifecycleTestEnv(t *testing.T) *lifecycle.Controller {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	life, err := newArchiveLifecycle(db, dbPath)
	require.NoError(t, err)
	life.Refresh(context.Background())
	return life
}

// 模块暂停时定时触发与定时维护直接跳过，恢复后照常执行。
func TestArchivePauseSkipsTimers(t *testing.T) {
	life := newLifecycleTestEnv(t)
	ctx := context.Background()
	calls := 0
	timer := gated(life, "archive_trigger", func(context.Context) error { calls++; return nil })
	require.NoError(t, timer(ctx))
	require.Equal(t, 1, calls)

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.NoError(t, timer(ctx))
	require.Equal(t, 1, calls, "暂停时定时触发直接跳过")

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.False(t, life.Halted(lifecycle.Ref{}), "没有要追赶的数据，恢复即放行")
	require.NoError(t, timer(ctx))
	require.Equal(t, 2, calls, "恢复后照常执行")
}
