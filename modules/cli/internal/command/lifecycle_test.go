package command

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// 暂停带原因与类型，恢复先进入追赶，追平后用 release 放行；输出里能看到暂停类型与追赶进度。全程经操作员网关调模块服务的两个方法。
func TestLifecyclePauseResumeReleaseAndCatchup(t *testing.T) {
	caught := false
	gateway := newLifecycleGateway()
	ctrl := gateway.add(t, "factor-engine", lifecycle.Hooks{
		Catchup: func(context.Context, time.Time) (lifecycle.Catchup, error) {
			return lifecycle.Catchup{Total: 10, Done: 4, CaughtUp: caught}, nil
		},
	})
	gateway.use(t)
	ctx := context.Background()
	module := lifecycleOptions{Module: "factor-engine"}

	var out bytes.Buffer
	require.NoError(t, runLifecycleStatus(ctx, module, &out))
	require.Contains(t, out.String(), "模块 factor-engine：运行")
	require.True(t, gateway.closed, "命令结束关闭网关")

	out.Reset()
	opts := lifecycleOptions{Module: "factor-engine", Reason: "切换行情源", PauseKind: "system", By: "ops"}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "暂停（系统暂停，ops）")
	require.Contains(t, out.String(), "原因：系统暂停：切换行情源")

	out.Reset()
	opts = lifecycleOptions{Module: "factor-engine", PauseKind: "system", ManualRelease: true}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdResume, &out))
	require.Contains(t, out.String(), "恢复追赶")
	require.Contains(t, out.String(), "追赶：4/10")

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, module, &out))
	require.Contains(t, out.String(), "追赶中")
	err := runLifecycleCommand(ctx, module, lifecycle.CmdRelease, &out)
	require.ErrorContains(t, err, "尚未追平", "没追平不能放行，错误是模块给的说明")

	caught = true
	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, module, &out))
	require.Contains(t, out.String(), "已追平，待放行")
	out.Reset()
	require.NoError(t, runLifecycleCommand(ctx, module, lifecycle.CmdRelease, &out))
	require.Contains(t, out.String(), "运行")
	require.Equal(t, lifecycle.StageRunning, ctrl.Stage())

	out.Reset()
	require.NoError(t, runLifecycleCatchup(ctx, module, &out))
	require.Contains(t, out.String(), "不在恢复追赶中")
}

// pause --for：带到期时间暂停，输出里能看到自动恢复的时间；系统暂停与 --for 同用被拒绝，--for 不能用于其他命令。
func TestLifecyclePauseForExpiry(t *testing.T) {
	gateway := newLifecycleGateway()
	ctrl := gateway.add(t, "trade", lifecycle.Hooks{})
	gateway.use(t)
	ctx := context.Background()

	var out bytes.Buffer
	opts := lifecycleOptions{Module: "trade", By: "ops", ExpiresIn: 2 * time.Hour}
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out))
	require.Contains(t, out.String(), "自动恢复")
	require.False(t, ctrl.State().Pause.ExpiresAt.IsZero())

	opts.PauseKind = "system"
	require.ErrorContains(t, runLifecycleCommand(ctx, opts, lifecycle.CmdPause, &out), "系统暂停不支持定时恢复")
	require.ErrorContains(t, runLifecycleCommand(ctx, lifecycleOptions{Module: "trade", ExpiresIn: time.Hour}, lifecycle.CmdResume, &out), "--for")
}

// factor-mgr、archive-mgr 不参与系统暂停，但可以单独暂停、恢复（经各自服务的两个方法）；没有部署、不认识的模块直接报错。
func TestLifecycleManagersAndUnknownModules(t *testing.T) {
	gateway := newLifecycleGateway()
	mgr := gateway.add(t, "factor-mgr", lifecycle.Hooks{})
	archive := gateway.add(t, "archive-mgr", lifecycle.Hooks{})
	gateway.use(t)
	ctx := context.Background()
	var out bytes.Buffer

	require.NoError(t, runLifecycleCommand(ctx, lifecycleOptions{Module: "factor-mgr", By: "ops"}, lifecycle.CmdPause, &out))
	require.True(t, mgr.Halted())
	require.NoError(t, runLifecycleCommand(ctx, lifecycleOptions{Module: "factor-mgr", By: "ops"}, lifecycle.CmdResume, &out))
	require.True(t, mgr.Running(), "没有要追赶的数据，恢复即放行")
	require.NoError(t, runLifecycleCommand(ctx, lifecycleOptions{Module: "archive-mgr", By: "ops"}, lifecycle.CmdPause, &out))
	require.True(t, archive.Halted())
	require.Equal(t, []string{"factor-mgr:pause", "factor-mgr:resume", "archive-mgr:pause"}, gateway.snapshot())

	require.ErrorContains(t, runLifecycleStatus(ctx, lifecycleOptions{Module: "strategy"}, &out), "没有部署")
	require.ErrorContains(t, runLifecycleStatus(ctx, lifecycleOptions{Module: "monitor"}, &out), "未知的模块")
}

// lifecycle 只剩模块级的命令：对象级的 init、start、confirm、check 与读影子目标的 shadow 都没有了；健康端口的地址参数也没有了。
func TestLifecycleHasOnlyModuleCommands(t *testing.T) {
	var names []string
	for _, child := range lifecycleCmd.Commands() {
		names = append(names, child.Name())
	}
	require.ElementsMatch(t, []string{"status", "pause", "resume", "release", "catchup"}, names)
	for _, flag := range []string{"task-id", "kind", "id", "force", "endpoint"} {
		require.Nil(t, lifecycleCmd.PersistentFlags().Lookup(flag), flag)
	}
	for _, flag := range []string{"collector", "factor", "strategy", "trade"} {
		require.Nil(t, systemCmd.PersistentFlags().Lookup(flag), flag)
	}
	require.Nil(t, importCmd.Flags().Lookup("engine"))
}

// 单模块 lifecycle pause 默认只预览不发暂停请求，加 --yes 才执行；预览阶段同样校验模块存在与部署状态。
func TestLifecyclePauseOnlyPreviewsWithoutYes(t *testing.T) {
	gateway := newLifecycleGateway()
	ctrl := gateway.add(t, "trade", lifecycle.Hooks{})
	gateway.use(t)

	pause, _, err := lifecycleCmd.Find([]string{"pause"})
	require.NoError(t, err)
	require.NotNil(t, pause.Flags().Lookup("yes"), "lifecycle pause 必须提供 --yes 确认参数")

	saved := lifecycleFlags
	t.Cleanup(func() { lifecycleFlags = saved })

	var out, errOut bytes.Buffer
	pause.SetOut(&out)
	pause.SetErr(&errOut)
	pause.SetContext(context.Background())

	lifecycleFlags = lifecycleOptions{Module: "trade", Reason: "维护", ExpiresIn: 2 * time.Hour, Yes: false}
	require.NoError(t, pause.RunE(pause, nil))
	preview := parsePreview(t, out.String())
	require.Equal(t, "lifecycle_pause", preview["action"])
	require.Equal(t, "trade", preview["module"])
	require.Equal(t, "manual", preview["pause_kind"])
	require.Equal(t, "维护", preview["reason"])
	require.Equal(t, "2h0m0s", preview["for"])
	require.Empty(t, gateway.snapshot(), "没有 --yes 不能发出暂停请求")
	require.True(t, ctrl.Running())

	out.Reset()
	lifecycleFlags.Yes = true
	require.NoError(t, pause.RunE(pause, nil))
	require.Equal(t, []string{"trade:pause"}, gateway.snapshot())
	require.True(t, ctrl.Halted())
	require.Contains(t, out.String(), "模块 trade：暂停")

	lifecycleFlags = lifecycleOptions{Module: "strategy", Yes: false}
	require.ErrorContains(t, pause.RunE(pause, nil), "没有部署")
	lifecycleFlags = lifecycleOptions{Module: "monitor", Yes: false}
	require.ErrorContains(t, pause.RunE(pause, nil), "未知的模块")
}
