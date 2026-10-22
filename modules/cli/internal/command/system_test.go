package command

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func lifecycleServer(t *testing.T, name string) (*httptest.Server, *lifecycle.Controller) {
	t.Helper()
	ctrl, err := lifecycle.New(lifecycle.Options{Module: name})
	require.NoError(t, err)
	ctrl.Refresh(context.Background())
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	mux.Handle(lifecycle.CatchupPath, ctrl.CatchupHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, ctrl
}

// 系统暂停与恢复端到端：命令行 → 编排器 → 各模块的 HTTP 接口 → 生命周期控制器。
func TestSystemPauseStatusResumeAgainstModules(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	collector, collectorCtrl := lifecycleServer(t, "collector")
	trade, tradeCtrl := lifecycleServer(t, "trade")
	opts := systemOptions{Collector: collector.URL, Trade: trade.URL, Reason: "切换行情源", By: "ops"}
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, runSystemPause(ctx, opts, &out))
	require.Contains(t, out.String(), "系统已暂停")
	require.Equal(t, lifecycle.StagePaused, collectorCtrl.Stage(lifecycle.Ref{}))
	require.Equal(t, lifecycle.StagePaused, tradeCtrl.Stage(lifecycle.Ref{}))

	out.Reset()
	require.NoError(t, runSystemStatus(ctx, opts, &out))
	require.Contains(t, out.String(), "collector")
	require.Contains(t, out.String(), "暂停")
	require.Contains(t, out.String(), "系统暂停：切换行情源")

	out.Reset()
	require.NoError(t, runSystemResume(ctx, opts, &out))
	require.Contains(t, out.String(), "系统已恢复")
	require.Equal(t, lifecycle.StageReady, collectorCtrl.Stage(lifecycle.Ref{}))
	require.Equal(t, lifecycle.StageReady, tradeCtrl.Stage(lifecycle.Ref{}))
	require.False(t, collectorCtrl.Halted(lifecycle.Ref{}))
}

func TestSystemCommandsValidateArguments(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	require.Error(t, runSystemStatus(ctx, systemOptions{}, &out), "没有任何模块地址")
	require.Error(t, runSystemPause(ctx, systemOptions{}, &out))
	require.Error(t, runSystemResume(ctx, systemOptions{ReleasePartial: true}, &out), "--release-partial 需要 --yes")
	require.Error(t, runSystemResume(ctx, systemOptions{}, &out), "没有任何模块地址")
}
