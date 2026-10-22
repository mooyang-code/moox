package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/sysctl"
	"github.com/stretchr/testify/require"
)

// resetModuleServer 起一个带重置的模块生命周期接口；服务端对所有请求授予 reset 权限（真实环境由重置密钥映射）。
func resetModuleServer(t *testing.T, name string) (*httptest.Server, *lifecycle.Controller) {
	t.Helper()
	ctrl, err := lifecycle.New(lifecycle.Options{
		Module: name, StateFile: filepath.Join(t.TempDir(), "lifecycle.json"),
		Hooks: lifecycle.Hooks{Reset: &lifecycle.ResetSpec{Plan: func(context.Context, lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
			return lifecycle.ResetPlanInfo{Keeps: []string{"身份"}}, []lifecycle.ResetStep{
				{Name: "backup", Describe: "备份", Run: func(context.Context, string) error { return nil }},
				{Name: "clear", Describe: "清表", Run: func(context.Context, string) error { return nil }},
			}, nil
		}}},
	})
	require.NoError(t, err)
	ctrl.Refresh(context.Background())
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	mux.Handle(lifecycle.CatchupPath, ctrl.CatchupHandler())
	mux.Handle(lifecycle.ResetPrefix, ctrl.ResetHandler())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(requestauth.WithPermissions(r.Context(), requestauth.PermissionReset)))
	}))
	t.Cleanup(srv.Close)
	return srv, ctrl
}

// 系统重置端到端：命令行 → 编排器 → 各模块的 HTTP 接口。生产环境要输入系统名；停在外部步骤，做完再继续。
func TestSystemResetEndToEnd(t *testing.T) {
	for k, v := range map[string]string{
		"MOOX_HEALTH_AUTH_ACCESS_KEY": "k", "MOOX_HEALTH_AUTH_SECRET_KEY": "s",
		"MOOX_HEALTH_RESET_ACCESS_KEY": "rk", "MOOX_HEALTH_RESET_SECRET_KEY": "rs",
	} {
		t.Setenv(k, v)
	}
	strategy, strategyCtrl := resetModuleServer(t, "strategy")
	monitor, monitorCtrl := resetModuleServer(t, "monitor")
	opts := systemOptions{Strategy: strategy.URL, Monitor: monitor.URL, By: "ops"}
	reset := systemResetOptions{Scope: "data", SystemName: "moox-prod", Env: "production", StateDir: t.TempDir(), StoragePkg: "/pkg", StorageConf: "/pkg/storage.yaml", AdminDB: "/admin.db"}
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, runSystemResetPlan(ctx, opts, reset, &out))
	require.Contains(t, out.String(), "[strategy]")
	require.Contains(t, out.String(), "[外部步骤 storage]")
	require.Contains(t, out.String(), "reset-view-consumers --reset-all-storage-data")
	m := regexp.MustCompile(`令牌（[^）]*）：(\w+)`).FindStringSubmatch(out.String())
	require.Len(t, m, 2, out.String())
	token := m[1]

	out.Reset()
	require.Error(t, runSystemResetApply(ctx, opts, systemResetOptions{StateDir: reset.StateDir, Env: "production", SystemName: "moox-prod", Token: token}, &out), "没有 --yes")
	require.ErrorContains(t, runSystemResetApply(ctx, opts, systemResetOptions{StateDir: reset.StateDir, Env: "production", SystemName: "moox-prod", Token: token, Yes: true}, &out), "系统名")

	apply := systemResetOptions{StateDir: reset.StateDir, Env: "production", SystemName: "moox-prod", Token: token, ConfirmName: "moox-prod", Yes: true, AdminDB: "/admin.db", StoragePkg: "/pkg", StorageConf: "/pkg/storage.yaml"}
	out.Reset()
	require.NoError(t, runSystemResetApply(ctx, opts, apply, &out), "停在外部步骤不算失败")
	require.Contains(t, out.String(), "请先完成外部步骤 storage")
	require.Contains(t, out.String(), "--external-done storage")
	require.Equal(t, lifecycle.StageUninitialized, strategyCtrl.Snapshot().Self.Stage)
	require.Equal(t, lifecycle.StagePaused, monitorCtrl.Snapshot().Self.Stage, "Monitor 排在 Storage 之后")

	data, err := os.ReadFile(filepath.Join(reset.StateDir, "journal.json"))
	require.NoError(t, err)
	var j sysctl.ResetJournal
	require.NoError(t, json.Unmarshal(data, &j))
	opID := j.OperationID
	out.Reset()
	require.NoError(t, runSystemResetStatus(opts, reset, &out))
	require.Contains(t, out.String(), opID)
	require.Contains(t, out.String(), "等待人工")

	out.Reset()
	cont := systemResetOptions{StateDir: reset.StateDir, Env: "production", SystemName: "moox-prod", OperationID: opID, ExternalDone: []string{"storage"}, Yes: true, AdminDB: "/admin.db"}
	require.NoError(t, runSystemResetApply(ctx, opts, cont, &out))
	require.Contains(t, out.String(), "请先完成外部步骤 admin")
	cont.ExternalDone = []string{"admin"}
	out.Reset()
	require.NoError(t, runSystemResetApply(ctx, opts, cont, &out))
	require.Contains(t, out.String(), "系统重置完成")
	require.Equal(t, lifecycle.StageUninitialized, monitorCtrl.Snapshot().Self.Stage)
	require.Eventually(t, func() bool { return monitorCtrl.ResetStatus().LastReset != nil }, time.Second, 10*time.Millisecond)
}

func TestSystemResetRequiresResetKeysAndModules(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	t.Setenv("MOOX_HEALTH_RESET_ACCESS_KEY", "")
	t.Setenv("MOOX_HEALTH_RESET_SECRET_KEY", "")
	strategy, _ := resetModuleServer(t, "strategy")
	reset := systemResetOptions{Scope: "data", SystemName: "n", Env: "production", StateDir: t.TempDir()}
	var out bytes.Buffer
	err := runSystemResetPlan(context.Background(), systemOptions{Strategy: strategy.URL}, reset, &out)
	require.Error(t, err)
	require.Contains(t, out.String(), "重置专用密钥")
	require.Error(t, runSystemResetPlan(context.Background(), systemOptions{}, reset, &out), "没有任何模块地址")
	reset.Env = "staging"
	require.ErrorContains(t, runSystemResetPlan(context.Background(), systemOptions{Strategy: strategy.URL}, reset, &out), "环境只能")
}
