package engine

import (
	"context"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

// EngineControl 的生命周期方法转给引擎的控制器；CheckTask 立即重查任务（结果写进随心跳上报的缓存），窗口没载入时唤醒预载，
// 任务不在目录里返回 NOT_FOUND。
func TestEngineControlLifecycleAndCheckTask(t *testing.T) {
	r, _ := newLifecycleRuntime(t)
	var svc factorpb.EngineControlService = &engineControl{r: r}
	ctx := context.Background()

	control, err := svc.ControlLifecycle(ctx, &commonpb.ControlLifecycleReq{Command: "pause", PauseKind: "system", By: "ops"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, control.GetRetInfo().GetCode(), control.GetRetInfo().GetMsg())
	require.True(t, r.life.Halted())
	get, err := svc.GetLifecycle(ctx, &commonpb.GetLifecycleReq{})
	require.NoError(t, err)
	require.Equal(t, "factor-engine", get.GetLifecycle().GetModule())
	require.Equal(t, "system", get.GetLifecycle().GetPause().GetKind())

	rsp, err := svc.CheckTask(ctx, &factorpb.CheckTaskReq{TaskId: "ftask_a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.GetPaused(), "引擎模块暂停中")
	var window string
	for _, check := range rsp.GetChecks() {
		if check.GetName() == windowCheckName {
			window = check.GetStatus()
		}
	}
	require.Equal(t, "FAIL", window, "窗口还没载入")
	cached, ok := r.checks.get("ftask_a")
	require.True(t, ok, "重查结果写进缓存，随心跳上报")
	require.Len(t, cached, len(rsp.GetChecks()))
	select {
	case <-r.preload.kick:
	default:
		t.Fatal("窗口没载入时应唤醒预载器")
	}

	missing, _ := svc.CheckTask(ctx, &factorpb.CheckTaskReq{TaskId: "ftask_x"})
	require.Equal(t, commonpb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
	empty, _ := svc.CheckTask(ctx, &factorpb.CheckTaskReq{})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, empty.GetRetInfo().GetCode())
}
