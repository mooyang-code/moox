package command

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/stretchr/testify/require"
)

// resetFixture 起一个带重置的控制器；withPermission 模拟服务端按密钥映射出的 reset 权限。
func resetFixture(t *testing.T, withPermission bool, fail *atomic.Bool) (*lifecycle.Controller, lifecycleOptions) {
	t.Helper()
	step := func(name string) lifecycle.ResetStep {
		return lifecycle.ResetStep{Name: name, Describe: "步骤 " + name, Run: func(context.Context, string) error {
			if name == "clear" && fail != nil && fail.Load() {
				return context.DeadlineExceeded
			}
			return nil
		}}
	}
	ctrl, err := lifecycle.New(lifecycle.Options{
		Module: "demo", StateFile: filepath.Join(t.TempDir(), "lifecycle.json"),
		Hooks: lifecycle.Hooks{Reset: &lifecycle.ResetSpec{Plan: func(context.Context, lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
			return lifecycle.ResetPlanInfo{Keeps: []string{"身份"}, Notes: []string{"不备份：大文件"}}, []lifecycle.ResetStep{step("backup"), step("clear")}, nil
		}}},
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	mux.Handle(lifecycle.ResetPrefix, ctrl.ResetHandler())
	var h http.Handler = mux
	if withPermission {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(requestauth.WithPermissions(r.Context(), requestauth.PermissionReset)))
		})
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	t.Setenv("MOOX_HEALTH_RESET_ACCESS_KEY", "rk")
	t.Setenv("MOOX_HEALTH_RESET_SECRET_KEY", "rs")
	ctrl.Refresh(context.Background())
	return ctrl, lifecycleOptions{Endpoint: srv.URL}
}

func extractToken(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "令牌") {
			return strings.TrimSpace(line[strings.Index(line, "：")+len("："):])
		}
	}
	t.Fatalf("输出里没有令牌：%s", out)
	return ""
}

// 完整流程：计划拿令牌 → 暂停 → 凭令牌发起 → 等到完成；没有 --yes 不执行，令牌只能用一次。
func TestResetCommandsFullFlow(t *testing.T) {
	ctrl, opts := resetFixture(t, true, nil)
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, runResetPlan(ctx, opts, resetOptions{Scope: "data"}, &out))
	require.Contains(t, out.String(), "1. backup")
	require.Contains(t, out.String(), "保留：身份")
	require.Contains(t, out.String(), "不备份：大文件")
	token := extractToken(t, out.String())

	r := resetOptions{Scope: "data", Token: token, Wait: true, Timeout: 20 * time.Second}
	require.ErrorContains(t, runResetApply(ctx, opts, r, &out), "--yes", "没有确认不执行")

	r.Yes = true
	require.Error(t, runResetApply(ctx, opts, r, &out), "模块没暂停，不能重置")

	_, err := ctrl.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseManual, By: "test"})
	require.NoError(t, err)

	out.Reset()
	require.NoError(t, runResetPlan(ctx, opts, resetOptions{Scope: "data"}, &out))
	r.Token = extractToken(t, out.String())
	out.Reset()
	require.NoError(t, runResetApply(ctx, opts, r, &out))
	require.Contains(t, out.String(), "重置完成")
	require.Contains(t, out.String(), "backup")

	out.Reset()
	require.NoError(t, runResetStatus(ctx, opts, &out))
	require.Contains(t, out.String(), "最近一次完成的重置")
	require.Error(t, runResetApply(ctx, opts, r, &out), "令牌只能用一次")
}

// 失败后提示续做；凭 operation_id 续做成功；没有重置权限或密钥时给出明确提示。
func TestResetCommandsFailureResumeAndPermission(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	ctrl, opts := resetFixture(t, true, &fail)
	ctx := context.Background()
	_, err := ctrl.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseManual, By: "test"})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runResetPlan(ctx, opts, resetOptions{Scope: "all"}, &out))
	token := extractToken(t, out.String())
	out.Reset()
	err = runResetApply(ctx, opts, resetOptions{Scope: "all", Token: token, Yes: true, Wait: true, Timeout: 20 * time.Second}, &out)
	require.ErrorContains(t, err, "重置失败")
	require.Contains(t, out.String(), "reset-apply --operation-id")
	opID := ctrl.ResetStatus().Operation.OperationID

	fail.Store(false)
	out.Reset()
	require.NoError(t, runResetApply(ctx, opts, resetOptions{OperationID: opID, Yes: true, Wait: true, Timeout: 20 * time.Second}, &out))
	require.Contains(t, out.String(), "重置完成")

	t.Setenv("MOOX_HEALTH_RESET_ACCESS_KEY", "")
	require.ErrorContains(t, runResetPlan(ctx, opts, resetOptions{Scope: "data"}, &out), "重置专用密钥")
	require.ErrorContains(t, runResetAbort(ctx, opts, resetOptions{Yes: true}, &out), "重置专用密钥")
	require.ErrorContains(t, runResetAbort(ctx, opts, resetOptions{}, &out), "--yes")
}

// 服务端没有给这对密钥 reset 权限时被拒绝。
func TestResetCommandsDeniedWithoutServerPermission(t *testing.T) {
	_, opts := resetFixture(t, false, nil)
	err := runResetPlan(context.Background(), opts, resetOptions{Scope: "data"}, &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reset")
}
