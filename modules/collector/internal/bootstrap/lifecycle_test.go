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

// 重启后载入的是暂停：lifecycle.json 本身就是唯一真相源，不写数据库暂停列；Run 开始时会触发一次回调通知组件同步。
func TestCollectorRestoredPausePreservesStageAndNotifiesWorkers(t *testing.T) {
	dir := t.TempDir()
	life, _, db, dbPath := newLifecycleFixtureAt(t, filepath.Join(dir, "collector.db"))
	ctx := context.Background()
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	// 数据库里只存手工暂停，系统暂停不写库。
	got, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.Empty(t, got)

	woke := false
	control := &collectorControl{plans: db.FetchPlans(), spaces: fixedSpaces("crypto", "stockcn"), wakeBackfill: func() { woke = true }}
	restarted, err := newCollectorLifecycle(dbPath, control)
	require.NoError(t, err)
	require.Equal(t, lifecycle.StagePaused, restarted.Stage())
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	restarted.Run(stopped, time.Hour)
	require.True(t, woke, "重启载入暂停阶段时通知后台循环同步")
}

// 暂停、恢复追赶与放行只改模块阶段并通知后台组件，不再修改数据库的手工暂停表。
func TestCollectorLifecycleDrivesPauseRecoveryAndRelease(t *testing.T) {
	life, control, db := newLifecycleFixture(t)
	wakes := 0
	control.wakeBackfill = func() { wakes++ }
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
	require.Empty(t, paused(), "系统暂停不写手工暂停表")
	require.Equal(t, 1, wakes)

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, life.Stage(), "没有补采组件时视为立即追平，但手动放行要等 release")
	require.Equal(t, 2, wakes)
	require.False(t, control.recoveryTo.IsZero(), "进入恢复追赶时记录追赶目标时刻")

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageRunning, life.Stage())
	require.Equal(t, 3, wakes)
	require.True(t, control.recoveryTo.IsZero())
	require.Empty(t, paused())
}

// 系统暂停再放行，不碰手工暂停的空间。
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
	control.setRecoveryTo(time.Now().UTC())

	state, err := control.catchup(ctx, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, state.CaughtUp, "追赶不等手工暂停的空间")
	require.Equal(t, []string{"crypto"}, plans.spaces, "只检查未手工暂停的空间")
	jobs, err := db.Backfill().ActiveJobs(ctx, "stockcn")
	require.NoError(t, err)
	require.Empty(t, jobs, "没有给手工暂停的空间提交补采作业")
	spaces, err := control.recoverySpaces(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto"}, spaces)
}

// 恢复追赶中再暂停：回到纯暂停（清空追赶目标，补采循环停止推进）。
func TestCollectorLifecyclePauseDuringRecoveryLeavesRecoveryMode(t *testing.T) {
	life, control, _ := newLifecycleFixture(t)
	ctx := context.Background()
	_, _ = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, life.Stage())
	require.False(t, control.recoveryTo.IsZero())
	_, _ = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.True(t, control.recoveryTo.IsZero())
	require.Equal(t, lifecycle.StagePaused, life.Stage())
}
