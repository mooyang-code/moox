package bootstrap

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func newLifecycleFixture(t *testing.T) (*lifecycle.Controller, *collectorControl, *store.Store) {
	t.Helper()
	life, control, db, _ := newLifecycleFixtureAt(t, filepath.Join(t.TempDir(), "collector.db"))
	return life, control, db
}

func newLifecycleFixtureAt(t *testing.T, dbPath string) (*lifecycle.Controller, *collectorControl, *store.Store, string) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	control := &collectorControl{}
	control.plans, control.spaces = db.FetchPlans(), fixedSpaces("crypto", "stockcn")
	life, err := newCollectorLifecycle(dbPath, control)
	require.NoError(t, err)
	return life, control, db, dbPath
}

// 重启后载入的是暂停：生命周期一开始运行就把系统暂停开关重新落到全部空间上（采集不能因为开关丢了自己重新开始）。
func TestCollectorRestoredPauseReappliesTheSystemPause(t *testing.T) {
	dir := t.TempDir()
	life, _, db, dbPath := newLifecycleFixtureAt(t, filepath.Join(dir, "collector.db"))
	ctx := context.Background()
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	for _, space := range []string{"crypto", "stockcn"} {
		require.NoError(t, db.FetchPlans().SetSystemPaused(ctx, space, false))
	}

	control := &collectorControl{}
	control.plans, control.spaces = db.FetchPlans(), fixedSpaces("crypto", "stockcn")
	restarted, err := newCollectorLifecycle(dbPath, control)
	require.NoError(t, err)
	require.Equal(t, lifecycle.StagePaused, restarted.Stage())
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	restarted.Run(stopped, time.Hour)
	got, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"crypto": true, "stockcn": true}, got)
}

// 暂停把全部采集计划空间置为暂停；恢复追赶进入恢复模式（暂停开关仍在，补采调度运行）；放行才清除暂停开关。
func TestCollectorLifecycleDrivesPauseRecoveryAndRelease(t *testing.T) {
	life, control, db := newLifecycleFixture(t)
	require.Equal(t, lifecycle.StageRunning, life.Stage())
	ctx := context.Background()
	paused := func() map[string]bool {
		got, err := db.FetchPlans().PausedSpaces(ctx)
		require.NoError(t, err)
		return got
	}
	require.Empty(t, paused())

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err, "没有绑定补采和计划管理器时没有需要等待的在途操作")
	require.Equal(t, lifecycle.StagePaused, life.Stage())
	require.Equal(t, map[string]bool{"crypto": true, "stockcn": true}, paused())
	require.False(t, control.Recovering("crypto"))

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, life.Stage(), "没有补采组件时视为立即追平，但手动放行要等 release")
	require.True(t, control.Recovering("crypto"), "恢复模式：补采运行，实时关闭")
	require.Len(t, paused(), 2, "暂停开关仍在，实时采集保持关闭")

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageRunning, life.Stage())
	require.False(t, control.Recovering("crypto"))
	require.Empty(t, paused(), "放行后清除暂停开关，对账协程恢复实时采集")
}

// 系统暂停再放行，只放开系统暂停的空间：之前手工暂停的空间保持暂停。
func TestCollectorLifecycleReleaseKeepsManuallyPausedSpaces(t *testing.T) {
	life, _, db := newLifecycleFixture(t)
	ctx := context.Background()
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "stockcn", true))

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageRunning, life.Stage())
	paused, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"stockcn": true}, paused)
}

// recordingPlans 记下读过时间线的空间。
type recordingPlans struct {
	mu     sync.Mutex
	spaces []string
}

func (p *recordingPlans) ListBySpace(_ context.Context, space string) ([]store.SeriesTimelineEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spaces = append(p.spaces, space)
	return nil, nil
}

type noRows struct{}

func (noRows) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return &storagepb.ReadTimeSeriesRowsRsp{}, nil
}

// 系统恢复追赶不碰手工暂停的空间：不检查它的缺口、不给它提交补采作业，追赶进度也不等它；补采调度对它保持暂停。
func TestCollectorRecoverySkipsManuallyPausedSpaces(t *testing.T) {
	_, control, db := newLifecycleFixture(t)
	ctx := context.Background()
	require.NoError(t, db.FetchPlans().SetPaused(ctx, "stockcn", true))
	plans := &recordingPlans{}
	control.backfiller = &klinecheck.Backfiller{Checker: &klinecheck.Checker{Timeline: plans, Rows: noRows{}}, Store: db.Backfill(), Routes: &fetchplan.BackfillRoutes{}}
	require.NoError(t, control.setPaused(ctx, true))
	control.setRecovering(true)

	state, err := control.catchup(ctx, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, state.CaughtUp, "追赶不等手工暂停的空间")
	require.Equal(t, []string{"crypto"}, plans.spaces, "只检查系统暂停的空间")
	jobs, err := db.Backfill().ActiveJobs(ctx, "stockcn")
	require.NoError(t, err)
	require.Empty(t, jobs, "没有给手工暂停的空间提交补采作业")
	spaces, err := control.recoverySpaces(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto"}, spaces)
}

// 恢复追赶中再暂停：回到纯暂停（恢复模式关闭，补采不再运行）。
func TestCollectorLifecyclePauseDuringRecoveryLeavesRecoveryMode(t *testing.T) {
	life, control, _ := newLifecycleFixture(t)
	ctx := context.Background()
	_, _ = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.True(t, control.Recovering("crypto"))
	_, _ = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.False(t, control.Recovering("crypto"))
	require.Equal(t, lifecycle.StagePaused, life.Stage())
}
