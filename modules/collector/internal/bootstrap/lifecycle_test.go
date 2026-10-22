package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newLifecycleFixture(t *testing.T) (*lifecycle.Controller, *collectorControl, *store.Store) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	control := &collectorControl{}
	control.plans, control.spaces = db.FetchPlans(), []string{"crypto", "stockcn"}
	life, err := newCollectorLifecycle(filepath.Join(t.TempDir(), "collector.db"), control, func() bool { return true }, nil)
	require.NoError(t, err)
	life.Refresh(context.Background())
	require.Equal(t, lifecycle.StageReady, life.Stage(lifecycle.Ref{}))
	return life, control, db
}

// 暂停把全部采集计划空间置为暂停；恢复追赶进入恢复模式（暂停开关仍在，补采调度运行）；放行才清除暂停开关。
func TestCollectorLifecycleDrivesPauseRecoveryAndRelease(t *testing.T) {
	life, control, db := newLifecycleFixture(t)
	ctx := context.Background()
	paused := func() map[string]bool {
		got, err := db.FetchPlans().PausedSpaces(ctx)
		require.NoError(t, err)
		return got
	}
	require.Empty(t, paused())

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err, "没有绑定补采和计划管理器时没有需要等待的在途操作")
	require.Equal(t, lifecycle.StagePaused, life.Stage(lifecycle.Ref{}))
	require.Equal(t, map[string]bool{"crypto": true, "stockcn": true}, paused())
	require.False(t, control.Recovering("crypto"))

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, life.Stage(lifecycle.Ref{}), "没有补采组件时视为立即追平，但手动放行要等 release")
	require.True(t, control.Recovering("crypto"), "恢复模式：补采运行，实时关闭")
	require.Len(t, paused(), 2, "暂停开关仍在，实时采集保持关闭")

	_, err = life.Command(ctx, lifecycle.CmdRelease, lifecycle.Ref{})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageReady, life.Stage(lifecycle.Ref{}))
	require.False(t, control.Recovering("crypto"))
	require.Empty(t, paused(), "放行后清除暂停开关，对账协程恢复实时采集")
}

// 恢复追赶中再暂停：回到纯暂停（恢复模式关闭，补采不再运行）。
func TestCollectorLifecyclePauseDuringRecoveryLeavesRecoveryMode(t *testing.T) {
	life, control, _ := newLifecycleFixture(t)
	ctx := context.Background()
	_, _ = life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.True(t, control.Recovering("crypto"))
	_, _ = life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	require.False(t, control.Recovering("crypto"))
	require.Equal(t, lifecycle.StagePaused, life.Stage(lifecycle.Ref{}))
}
