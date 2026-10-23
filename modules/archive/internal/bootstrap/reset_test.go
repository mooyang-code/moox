package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newArchiveResetEnv(t *testing.T) (*lifecycle.Controller, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	life, err := newArchiveLifecycle(db, dbPath)
	require.NoError(t, err)
	life.Refresh(context.Background())
	return life, db
}

func TestArchiveResetClassifiesEveryTable(t *testing.T) {
	_, db := newArchiveResetEnv(t)
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, archiveDataTables, archiveConfigTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 暂停时定时触发与维护跳过；有排队或执行中的运行时拒绝重置；没有则清表并备份。
func TestArchivePauseSkipsTimersAndResetRefusesActiveRuns(t *testing.T) {
	life, db := newArchiveResetEnv(t)
	ctx := context.Background()
	calls := 0
	timer := gated(life, "archive_trigger", func(context.Context) error { calls++; return nil })
	require.NoError(t, timer(ctx))
	require.Equal(t, 1, calls)

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.NoError(t, timer(ctx))
	require.Equal(t, 1, calls, "暂停时定时触发直接跳过")

	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_freq, c_partition_unit, c_schedule_cron, c_interval_seconds, c_start_at)
		VALUES ('crypto', 'ds', '1m', 'day', '0 2 * * *', 86400, CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_status, c_scheduled_at)
		VALUES ('r1', 'crypto', 'ds', 'scheduled', 'running', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	plan, err := life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)
	require.Contains(t, plan.Blockers[0], "排队或执行中")
	require.Empty(t, plan.Token)

	_, err = sqlDB.Exec(`UPDATE t_archive_runs SET c_status = 'succeeded'`)
	require.NoError(t, err)
	plan, err = life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Equal(t, "done", life.ResetStatus().Operation.State)
	var n int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM t_archive_runs").Scan(&n))
	require.Zero(t, n)
}
