package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// 引擎进程内重置的端到端：暂停 → plan → apply，本地时序存储、连续水位、空洞账本、内存数据源被清掉，
// 小文件备份在 reset-backups；all 范围再清目录缓存；之后任务要重新 init 才能用。
func TestEngineResetClearsLocalStateInProcess(t *testing.T) {
	ctx := context.Background()
	r, _ := newLifecycleRuntime(t)
	dir := filepath.Dir(r.cfg.Lifecycle.StateFile)

	series, err := seriesstore.Open(seriesstore.Options{Dir: filepath.Join(dir, "seriesstore"), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = series.Close() })
	r.series = series
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	cols := seriesstore.NewColumns([]seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64, Fill: seriesstore.FillPrevClose}}, 1)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cols.AppendRow(t0, "", false, seriesstore.Float(100))
	_, err = series.WriteRange(ctx, src, seriesstore.SubjectWrite{Subject: "BTC", From: t0, To: t0.Add(time.Minute), Rows: cols}, seriesstore.WriteOptions{})
	require.NoError(t, err)

	r.watermarks, err = watermark.Open(filepath.Join(dir, "watermarks.json"))
	require.NoError(t, err)
	require.NoError(t, r.watermarks.Put(watermark.State{TaskID: "ftask_a", CommittedThrough: t0, Target: t0}))
	r.cfg.CatalogSync.StateFile = r.catalog.stateFile // 夹具里 Apply 已经写出了目录缓存文件，备份时要能拷到
	r.cfg.Gap.LedgerDir = filepath.Join(dir, "gap")
	ledger, _, err := gapledger.Open(r.cfg.Gap.LedgerDir, time.Now, 0)
	require.NoError(t, err)
	require.NoError(t, ledger.RecordGaps(ctx, []pipeline.GapEntry{{TaskID: "ftask_a", Subject: "BTC", Period: t0, Member: "f", DefinitionHash: "h", Reason: pipeline.SkipReasonUpstream}}))
	r.gap = ledger
	require.Equal(t, 1, ledger.PendingCount())
	r.data = localdata.New(localdata.Config{})
	r.preload.data = &preloadDataFake{}
	require.NoError(t, r.preload.PrepareSet(ctx, "ftask_a"))
	require.True(t, r.preload.Ready("ftask_a"))

	r.life.Refresh(ctx)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)

	plan, err := r.life.ResetPlan(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.Equal(t, 1, plan.Stats["watermarks"])
	require.Equal(t, 1, plan.Stats["gap_pending"])
	require.Contains(t, plan.Notes[0], "本地时序存储不备份")
	_, err = r.life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetData, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, r.life.ResetWait(ctx))
	require.Equal(t, "done", r.life.ResetStatus().Operation.State)

	subjects, err := series.Subjects(src)
	require.NoError(t, err)
	require.Empty(t, subjects, "本地时序存储被清空")
	require.Empty(t, r.watermarks.All(), "连续水位被清空")
	require.Zero(t, ledger.PendingCount(), "空洞账本被清空")
	require.False(t, r.preload.Ready("ftask_a"), "预载记录被忘掉，任务要重新 init")
	require.Equal(t, lifecycle.StageUninitialized, r.life.Stage(lifecycle.Ref{}))
	require.False(t, r.gate("ftask_a"))

	backups, err := filepath.Glob(filepath.Join(dir, "reset-backups", "*", "watermarks.json"))
	require.NoError(t, err)
	require.Len(t, backups, 1, "水位文件备份在 reset-backups")
	raw, err := os.ReadFile(backups[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), "ftask_a", "备份的是重置前的内容")

	// 重置后还能 init，数据层重新工作。
	obj, err := r.life.Command(ctx, lifecycle.CmdInit, lifecycle.Ref{})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageReady, obj.Stage)
}

// 重置期间预载循环不会把数据拉回来；模块暂停、恢复追赶、重置时预载都被挡住。
func TestPreloaderIsBlockedWhileTheModuleIsHalted(t *testing.T) {
	r, fake := newLifecycleRuntime(t)
	ctx := context.Background()
	r.preload.blocked = func() bool { return r.life != nil && r.life.Halted(lifecycle.Ref{}) }
	r.life.Refresh(ctx)
	r.lease = &leaseState{}

	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	r.preload.once(ctx)
	require.Empty(t, fake.prepared, "暂停时预载循环不运行")

	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !r.life.Halted(lifecycle.Ref{}) }, 3*time.Second, 20*time.Millisecond)
	r.preload.once(ctx)
	require.NotEmpty(t, fake.prepared, "放行后预载循环恢复")
}
