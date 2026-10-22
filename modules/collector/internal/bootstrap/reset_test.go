package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newCollectorResetEnv(t *testing.T) (*lifecycle.Controller, *collectorControl, *store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "moox_collector.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	control := &collectorControl{}
	control.plans, control.spaces = db.FetchPlans(), []string{"crypto"}
	life, err := newCollectorLifecycle(dbPath, control, func() bool { return true }, collectorResetSpec(db, dbPath, control))
	require.NoError(t, err)
	life.Refresh(context.Background())
	return life, control, db, dbPath
}

func TestCollectorResetClassifiesEveryTable(t *testing.T) {
	_, _, db, _ := newCollectorResetEnv(t)
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, collectorDataTables, collectorConfigTables, collectorStateTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 暂停开关两个范围都不清；重置后第一次 init 才放开（AfterReset），之前采集一直保持暂停。
func TestCollectorResetKeepsPauseFlagUntilTheFirstInit(t *testing.T) {
	life, _, db, _ := newCollectorResetEnv(t)
	ctx := context.Background()
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_collector_backfill_watermarks (c_space_id, c_dataset_id, c_frequency, c_scanned_until) VALUES ('crypto', 'ds', '1m', CURRENT_TIMESTAMP)`)
	require.NoError(t, err)

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	paused, err := db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.True(t, paused["crypto"])

	plan, err := life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)
	require.Len(t, plan.Steps, 3)
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Equal(t, lifecycle.StageUninitialized, life.Stage(lifecycle.Ref{}))
	var left int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM t_collector_backfill_watermarks").Scan(&left))
	require.Zero(t, left, "运行数据被清掉")

	paused, err = db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.True(t, paused["crypto"], "重置期间与重置完成后暂停开关保持，采集不会自己重新开始")

	_, err = life.Command(ctx, lifecycle.CmdInit, lifecycle.Ref{})
	require.NoError(t, err)
	paused, err = db.FetchPlans().PausedSpaces(ctx)
	require.NoError(t, err)
	require.Empty(t, paused, "重置后第一次 init 放开采集")
}

// SCF 定时器还没有确认停用时拒绝重置：删掉采集计划后定时器会成为没人管理的孤儿。
func TestCollectorResetBlocksWhileTimersAreStillEnabled(t *testing.T) {
	life, control, _, _ := newCollectorResetEnv(t)
	control.manager = &marketfetch.PlanManager{}
	plan, err := life.ResetPlan(context.Background(), lifecycle.ResetData)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)
	require.Contains(t, plan.Blockers[0], "crypto")
	require.Contains(t, plan.Blockers[0], "孤儿")
	require.Empty(t, plan.Token)
}
