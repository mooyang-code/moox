package engine

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// engineControlService 是引擎入站服务 EngineControl 的服务名：只监听本机，由因子机的主机网关按路由转发。
// 回放只给策略服务调用；生命周期、任务检查与本地写入只给 moox-cli（见服务目录）。
const engineControlService = "trpc.moox.factor.EngineControl"

// engineControl 实现 EngineControl：回放在 replayService，本地写入在 localWriter，生命周期与任务检查在这里转给运行时。
// 失败都放在 ret_info 里返回，传输层只在网关转发失败时报错。
type engineControl struct {
	replayService
	*localWriter
	r *Runtime
}

var _ factorpb.EngineControlService = (*engineControl)(nil)

func (r *Runtime) controlService() *engineControl {
	return &engineControl{replayService: replayService{m: r.replays}, localWriter: newLocalWriter(r.data, r.storage), r: r}
}

// GetLifecycle 读引擎模块的暂停与恢复追赶状态。
func (c *engineControl) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return c.r.life.GetLifecycle(ctx, req)
}

// ControlLifecycle 暂停、恢复与放行引擎模块。
func (c *engineControl) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return c.r.life.ControlLifecycle(ctx, req)
}

// CheckTask 立即重查一个任务（与每 30 秒一轮的检查相同），结果写进缓存、随心跳上报；窗口没载入时唤醒预载。
func (c *engineControl) CheckTask(ctx context.Context, req *factorpb.CheckTaskReq) (*factorpb.CheckTaskRsp, error) {
	taskID := strings.TrimSpace(req.GetTaskId())
	if taskID == "" {
		return &factorpb.CheckTaskRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "task_id 不能为空"}}, nil
	}
	if _, lookup := c.r.catalog.SetByID(taskID); lookup != SetFound {
		return &factorpb.CheckTaskRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND,
			Msg: "因子任务 " + taskID + " 不在引擎的目录里：没有启用、结果数据集还没就绪，或目录还没有同步"}}, nil
	}
	checks := c.r.checkTask(ctx, taskID)
	c.r.checks.put(taskID, checks)
	if !c.r.preload.Ready(taskID) {
		c.r.preload.Kick()
	}
	return &factorpb.CheckTaskRsp{RetInfo: replaySuccess(), Checks: factorwire.TaskChecksToPB(checks), Paused: c.r.life != nil && c.r.life.Halted()}, nil
}
