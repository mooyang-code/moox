package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
	require.Len(t, plan.Steps, 4)
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

// SCF 定时器还没有确认停用时，重置在第一步失败而不是删除采集计划：否则定时器会成为没人管理的孤儿。
// plan 阶段（模块还在运行）不把它当阻止项，否则永远发不出令牌。
func TestCollectorResetFailsFirstStepWhileTimersAreStillEnabled(t *testing.T) {
	life, control, _, _ := newCollectorResetEnv(t)
	control.manager = &marketfetch.PlanManager{}
	control.timerWait = 50 * time.Millisecond
	ctx := context.Background()
	plan, err := life.ResetPlan(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers, "plan 阶段发得出令牌")
	require.NotEmpty(t, plan.Token)
	require.Equal(t, "verify_scf_timers_disabled", plan.Steps[0].Name)

	_, err = life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	if err != nil {
		t.Logf("暂停返回：%v", err) // 定时器没有停用时暂停会提示在途操作没结束，但门禁已关闭
	}
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetData, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	st := life.ResetStatus()
	require.Equal(t, "failed", st.Operation.State)
	require.Contains(t, st.Operation.Error, "孤儿")
	require.Equal(t, "failed", st.Operation.Steps[0].State)
	require.Equal(t, "pending", st.Operation.Steps[1].State, "后面的步骤没有执行")
}
