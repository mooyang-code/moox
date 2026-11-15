package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// controllerInvoker 是测试用的网关：按方法名把请求交给控制器的 RPC 实现，请求与响应都过一遍 protobuf 编码，和经网关一样。
type controllerInvoker struct {
	c       *Controller
	service string
	err     error
}

func (g *controllerInvoker) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	if g.err != nil {
		return g.err
	}
	if service != g.service {
		return errors.New("service has no enabled deployment")
	}
	raw, err := proto.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	var out proto.Message
	switch method {
	case GetMethod:
		in := &commonpb.GetLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		out, _ = g.c.GetLifecycle(ctx, in)
	case ControlMethod:
		in := &commonpb.ControlLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		out, _ = g.c.ControlLifecycle(ctx, in)
	default:
		return errors.New("unknown method " + method)
	}
	encoded, err := proto.Marshal(out)
	if err != nil {
		return err
	}
	return proto.Unmarshal(encoded, rsp.(proto.Message))
}

// 经网关客户端读状态、执行命令：暂停类型、原因、操作者、到期时间与追赶进度原样往返；不允许的命令带回模块的说明与当前状态。
func TestRemoteRoundTripsStateAndCommands(t *testing.T) {
	f := &fakeModule{}
	c := newController(t, f, "")
	remote := &Remote{Gateway: &controllerInvoker{c: c, service: "trpc.moox.factor.EngineControl"}, Service: "trpc.moox.factor.EngineControl"}
	ctx := context.Background()

	state, err := remote.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "factor-engine", state.Module)
	require.Equal(t, StageRunning, state.Stage)
	require.True(t, state.CaughtUp())

	expires := t0.Add(2 * time.Hour)
	state, err = remote.Exec(ctx, Request{Command: CmdPause, Reason: "维护", By: "ops", ExpiresAt: expires})
	require.NoError(t, err)
	require.Equal(t, StagePaused, state.Stage)
	require.Equal(t, PauseManual, state.Pause.Kind)
	require.Equal(t, "ops", state.Pause.By)
	require.True(t, state.Pause.ExpiresAt.Equal(expires))
	require.True(t, state.Quiesced(), "收敛时间原样往返")

	state, err = remote.Exec(ctx, Request{Command: CmdRelease})
	require.Error(t, err, "暂停中不能放行")
	require.Contains(t, err.Error(), "不能执行 release")
	require.Equal(t, StagePaused, state.Stage, "被拒绝时也带回当前状态")

	state, err = remote.Exec(ctx, Request{Command: CmdResume, ManualRelease: true})
	require.NoError(t, err)
	require.Equal(t, StageResuming, state.Stage)
	require.Equal(t, int64(10), state.Catchup.Total)
	require.False(t, state.CaughtUp())

	f.setCaught(true)
	state, err = remote.Status(ctx)
	require.NoError(t, err)
	require.False(t, state.CaughtUp(), "只读状态不推进追赶")
	state, err = remote.Catchup(ctx)
	require.NoError(t, err)
	require.True(t, state.CaughtUp(), "读追赶进度先推进一次追赶，进度是最新的")
	require.Equal(t, StageResuming, state.Stage, "手动放行：停在待放行")

	state, err = remote.Exec(ctx, Request{Command: CmdRelease})
	require.NoError(t, err)
	require.Equal(t, StageRunning, state.Stage)
	require.False(t, state.ReleasedAt.IsZero())

	_, err = (&Remote{Gateway: &controllerInvoker{err: errors.New("connection refused")}, Service: "x"}).Status(ctx)
	require.ErrorContains(t, err, "connection refused")
}

// 服务端的返回码：请求不合法 INVALID_PARAM，状态不允许 CONFLICT，在途操作没结束 INNER_ERR（说明原文保留，编排器据此判断）；
// 控制器还没建好时两个方法都返回 INNER_ERR。
func TestControlLifecycleReturnCodes(t *testing.T) {
	ctx := context.Background()
	c := newController(t, &fakeModule{}, "")
	for _, req := range []*commonpb.ControlLifecycleReq{
		{Command: "init"}, {Command: "pause", PauseKind: "auto"}, {Command: "pause", ExpiresAt: "明天"},
	} {
		rsp, err := c.ControlLifecycle(ctx, req)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), req.String())
		require.Equal(t, "running", rsp.GetLifecycle().GetStage())
	}
	rsp, _ := c.ControlLifecycle(ctx, &commonpb.ControlLifecycleReq{Command: "resume"})
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())

	slow, err := New(Options{Module: "trade", QuiesceTimeout: time.Millisecond, Hooks: Hooks{Quiesce: func(ctx context.Context) error {
		<-ctx.Done()
		return errors.New("仍有 1 个目标在收敛")
	}}})
	require.NoError(t, err)
	rsp, _ = slow.ControlLifecycle(ctx, &commonpb.ControlLifecycleReq{Command: "pause", PauseKind: "system"})
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "已暂停，但在途操作没有结束")
	require.Equal(t, "paused", rsp.GetLifecycle().GetStage(), "门禁已经关闭")

	var none *Controller
	get, _ := none.GetLifecycle(ctx, &commonpb.GetLifecycleReq{})
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, get.GetRetInfo().GetCode())
	control, _ := none.ControlLifecycle(ctx, &commonpb.ControlLifecycleReq{Command: "pause"})
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, control.GetRetInfo().GetCode())
}
