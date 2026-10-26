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

func TestLifecycleCommandsAgainstController(t *testing.T) {
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "factor-engine", Hooks: lifecycle.Hooks{
		Objects: func() []lifecycle.Ref { return []lifecycle.Ref{{Kind: "factor_task", ID: "ftask_a"}} },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check {
			return []lifecycle.Check{{Name: "本地窗口", Status: lifecycle.CheckFail, Message: "窗口还没有载入完成", Remedy: "moox-cli lifecycle init --task ftask_a"}}
		},
	}})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	ctx := context.Background()
	ctrl.Refresh(ctx)

	opts := lifecycleOptions{Endpoint: srv.URL, Task: "ftask_a"}
	var out bytes.Buffer
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdInit, &out))
	require.Contains(t, out.String(), "未就绪")
	require.Contains(t, out.String(), "处理：moox-cli lifecycle init --task ftask_a")

	out.Reset()
	require.NoError(t, runLifecycleStatus(ctx, lifecycleOptions{Endpoint: srv.URL}, &out))
	require.Contains(t, out.String(), "factor_task/ftask_a")

	require.Error(t, runLifecycleCommand(ctx, opts, lifecycle.CmdStart, &out), "未就绪的集合不能启动")
	_, err = lifecycleOptions{Task: "a", Kind: "x", ID: "y"}.ref()
	require.Error(t, err)
}

// 暂停带原因与类型，恢复先进入追赶，追平后用 release 放行；输出里能看到暂停类型与追赶进度。
func TestLifecyclePauseResumeReleaseAndCatchup(t *testing.T) {
	ref := lifecycle.Ref{Kind: "factor_task", ID: "ftask_a"}
	caught := false
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "factor-engine", Hooks: lifecycle.Hooks{
		Objects:     func() []lifecycle.Ref { return []lifecycle.Ref{ref} },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check { return nil },
		Catchup: func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error) {
			return lifecycle.Catchup{Total: 10, Done: 4, CaughtUp: caught}, nil
		},
	}})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	mux.Handle(lifecycle.CatchupPath, ctrl.CatchupHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	ctx := context.Background()
	ctrl.Refresh(ctx)
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		_, err := ctrl.Command(ctx, cmd, ref)
		require.NoError(t, err)
	}

	var out bytes.Buffer
	opts := lifecycleOptions{Endpoint: srv.URL, Task: "ftask_a", Reason: "切换行情源", PauseKind: "system", By: "ops"}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "暂停")
	require.Contains(t, out.String(), "系统暂停，ops")

	out.Reset()
	opts = lifecycleOptions{Endpoint: srv.URL, Task: "ftask_a", PauseKind: "system", ManualRelease: true}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdResume, &out))
	require.Contains(t, out.String(), "恢复追赶")
	require.Contains(t, out.String(), "追赶：4/10")

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: srv.URL}, &out))
	require.Contains(t, out.String(), "追赶中")
	require.Error(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: srv.URL, Task: "ftask_a"}, lifecycle.CmdRelease, &out), "没追平不能放行")

	caught = true
	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: srv.URL}, &out))
	require.Contains(t, out.String(), "已全部追平，待放行")
	out.Reset()
	require.NoError(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: srv.URL, Task: "ftask_a"}, lifecycle.CmdRelease, &out))
	require.Contains(t, out.String(), "运行")
	require.Equal(t, lifecycle.StageRunning, ctrl.Stage(ref))

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, lifecycleOptions{Endpoint: srv.URL}, &out))
	require.Contains(t, out.String(), "没有恢复追赶中的对象")
}

// pause --for：带到期时间暂停，输出里能看到自动恢复的时间；系统暂停与 --for 同用被拒绝，--for 不能用于其他命令。
func TestLifecyclePauseForExpiry(t *testing.T) {
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "trade"})
	require.NoError(t, err)
	ctrl.Refresh(context.Background())
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	ctx := context.Background()

	var out bytes.Buffer
	opts := lifecycleOptions{Endpoint: srv.URL, By: "ops", ExpiresIn: 2 * time.Hour}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "自动恢复")
	require.False(t, ctrl.Snapshot().Self.Pause.ExpiresAt.IsZero())

	opts.PauseKind = "system"
	require.ErrorContains(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out), "系统暂停不支持定时恢复")
	require.ErrorContains(t, runLifecycleCommand(ctx, lifecycleOptions{Endpoint: srv.URL, ExpiresIn: time.Hour}, lifecycle.CmdResume, &out), "--for")
}
