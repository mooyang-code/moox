package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func newMonitorResetEnv(t *testing.T) (*lifecycle.Controller, *store.Store, *Runtime) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "monitor.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.SQL()))
	t.Cleanup(func() { _ = db.Close() })
	life, err := newMonitorLifecycle(db, dbPath, nil)
	require.NoError(t, err)
	life.Refresh(context.Background())
	runtime := &Runtime{Store: db, Repositories: db.Repositories()}
	runtime.life.Store(life)
	return life, db, runtime
}

func TestMonitorResetClassifiesEveryTable(t *testing.T) {
	_, db, _ := newMonitorResetEnv(t)
	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, monitorDataTables, monitorConfigTables, monitorBootstrapTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 暂停时告警静默（发送器为 nil：只记录、不推送），放行后恢复；重置不清通知渠道（Webhook 是凭据）。
func TestMonitorPauseSilencesAlertsAndResetKeepsChannels(t *testing.T) {
	life, db, runtime := newMonitorResetEnv(t)
	ctx := context.Background()
	_, err := runtime.Repositories.Notifications.Add(ctx, "wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd1234")
	require.NoError(t, err)

	sender, err := runtime.alertSender(ctx)
	require.NoError(t, err)
	require.NotNil(t, sender, "正常时发给全部渠道")

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	sender, err = runtime.alertSender(ctx)
	require.NoError(t, err)
	require.Nil(t, sender, "暂停时告警静默：只记录、不推送")

	sqlDB, err := db.SQLDB()
	require.NoError(t, err)
	plan, err := life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Contains(t, plan.Keeps[1], "通知渠道")
	_, err = life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, life.ResetWait(ctx))
	require.Equal(t, "done", life.ResetStatus().Operation.State)

	var channels int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM t_monitor_notification_channels").Scan(&channels))
	require.Equal(t, 1, channels, "通知渠道保留")
	sender, err = runtime.alertSender(ctx)
	require.NoError(t, err)
	require.Nil(t, sender, "重置完成、init 之前仍然静默")

	_, err = life.Command(ctx, lifecycle.CmdInit, lifecycle.Ref{})
	require.NoError(t, err)
	sender, err = runtime.alertSender(ctx)
	require.NoError(t, err)
	require.NotNil(t, sender, "init 后恢复告警推送")
}
