package command

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func lifecycleTestServer(t *testing.T, ctrl *lifecycle.Controller) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	mux.Handle(lifecycle.CatchupPath, ctrl.CatchupHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	return srv.URL
}

// 暂停带原因与类型，恢复先进入追赶，追平后用 release 放行；输出里能看到暂停类型与追赶进度。
func TestLifecyclePauseResumeReleaseAndCatchup(t *testing.T) {
	caught := false
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "factor-engine", Hooks: lifecycle.Hooks{
		Catchup: func(context.Context, time.Time) (lifecycle.Catchup, error) {
			return lifecycle.Catchup{Total: 10, Done: 4, CaughtUp: caught}, nil
		},
	}})
	require.NoError(t, err)
	endpoint := lifecycleTestServer(t, ctrl)
	ctx := context.Background()

	var out bytes.Buffer
	require.NoError(t, runLifecycleStatus(ctx, lifecycleOptions{Endpoint: endpoint}, &out))
	require.Contains(t, out.String(), "模块 factor-engine：运行")

	out.Reset()
	opts := lifecycleOptions{Endpoint: endpoint, Reason: "切换行情源", PauseKind: "system", By: "ops"}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "暂停（系统暂停，ops）")
	require.Contains(t, out.String(), "原因：系统暂停：切换行情源")

	out.Reset()
	opts = lifecycleOptions{Endpoint: endpoint, PauseKind: "system", ManualRelease: true}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdResume, &out))
	require.Contains(t, out.String(), "恢复追赶")
	require.Contains(t, out.String(), "追赶：4/10")

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: endpoint}, &out))
	require.Contains(t, out.String(), "追赶中")
	require.Error(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: endpoint}, lifecycle.CmdRelease, &out), "没追平不能放行")

	caught = true
	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: endpoint}, &out))
	require.Contains(t, out.String(), "已追平，待放行")
	out.Reset()
	require.NoError(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: endpoint}, lifecycle.CmdRelease, &out))
	require.Contains(t, out.String(), "运行")
	require.Equal(t, lifecycle.StageRunning, ctrl.Stage())

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: endpoint}, &out))
	require.Contains(t, out.String(), "不在恢复追赶中")
}

// pause --for：带到期时间暂停，输出里能看到自动恢复的时间；系统暂停与 --for 同用被拒绝，--for 不能用于其他命令。
func TestLifecyclePauseForExpiry(t *testing.T) {
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "trade"})
	require.NoError(t, err)
	endpoint := lifecycleTestServer(t, ctrl)
	ctx := context.Background()

	var out bytes.Buffer
	opts := lifecycleOptions{Endpoint: endpoint, By: "ops", ExpiresIn: 2 * time.Hour}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "自动恢复")
	require.False(t, ctrl.State().Pause.ExpiresAt.IsZero())

	opts.PauseKind = "system"
	require.ErrorContains(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out), "系统暂停不支持定时恢复")
	require.ErrorContains(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: endpoint, ExpiresIn: time.Hour}, lifecycle.CmdResume, &out), "--for")
}

// lifecycle 只剩模块级的命令：对象级的 init、start、confirm、check 与读影子目标的 shadow 都没有了。
func TestLifecycleHasOnlyModuleCommands(t *testing.T) {
	var names []string
	for _, child := range lifecycleCmd.Commands() {
		names = append(names, child.Name())
	}
	require.ElementsMatch(t, []string{"status", "pause", "resume", "release", "catchup"}, names)
	for _, flag := range []string{"task-id", "kind", "id", "force"} {
		require.Nil(t, lifecycleCmd.PersistentFlags().Lookup(flag), flag)
	}
}
