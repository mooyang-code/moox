package command

import (
	"bytes"
	"context"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/errs"
)

// statusGateway 模拟因子管理的 GetStatus：一个任务窗口还没载入，一个任务检查通过。
type statusGateway struct {
	runners []*factorpb.FactorRunnerStatus
}

func (g *statusGateway) Forward(_ context.Context, service, method string, _ int, _ []byte) ([]byte, error) {
	return nil, errs.New(errs.RetServerNoFunc, service+"/"+method)
}

func (g *statusGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if service == engineControl && method == "CheckTask" {
		out := rsp.(*factorpb.CheckTaskRsp)
		if req.(*factorpb.CheckTaskReq).GetTaskId() != "ftask_a" {
			out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "因子任务不在引擎的目录里"}
			return nil
		}
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.Checks = []*factorpb.FactorTaskCheck{{Name: "本地窗口", Status: "PASS"}, {Name: "本地数据覆盖", Status: "PASS"}}
		return nil
	}
	if method != "GetStatus" {
		return errs.New(errs.RetServerNoFunc, method)
	}
	out := rsp.(*factorpb.GetStatusRsp)
	out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	out.Engine = &factorpb.EngineInfo{Online: true}
	out.Runners = g.runners
	return nil
}

func (g *statusGateway) Close() error { return nil }

// factor task status 列出引擎上报的逐项检查：没通过的项带原因与处理办法，通过的任务显示计算中。
func TestFactorTaskStatusShowsChecks(t *testing.T) {
	gateway := &statusGateway{runners: []*factorpb.FactorRunnerStatus{
		{TaskId: "ftask_a", Checks: []*factorpb.FactorTaskCheck{
			{Name: "本地窗口", Status: "FAIL", Message: "窗口还没有载入完成", Remedy: "引擎会自动载入"},
			{Name: "变更同步", Status: "PASS"},
		}},
		{TaskId: "ftask_b", Checks: []*factorpb.FactorTaskCheck{{Name: "本地窗口", Status: "PASS"}}},
		{TaskId: "ftask_c"},
	}}
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, runFactorTaskStatus(ctx, gateway, "", false, &out))
	text := out.String()
	require.Contains(t, text, "ftask_a：不计算：1 项检查没通过")
	require.Contains(t, text, "[FAIL] 本地窗口：窗口还没有载入完成")
	require.Contains(t, text, "处理：引擎会自动载入")
	require.NotContains(t, text, "变更同步", "通过的项不列出")
	require.Contains(t, text, "ftask_b：计算中")
	require.Contains(t, text, "ftask_c：引擎还没有上报检查结果")

	out.Reset()
	require.NoError(t, runFactorTaskStatus(ctx, gateway, "ftask_b", true, &out))
	require.Contains(t, out.String(), `"computing": true`)
	require.NotContains(t, out.String(), "ftask_a")

	gateway.runners[1].Paused = true
	out.Reset()
	require.NoError(t, runFactorTaskStatus(ctx, gateway, "ftask_b", false, &out))
	require.Contains(t, out.String(), "引擎模块暂停中")

	require.ErrorContains(t, runFactorTaskStatus(ctx, gateway, "missing", false, &out), "没有上报因子任务 missing")
}

// --recheck 直接问引擎：经 EngineControl.CheckTask 立即重查，不读心跳里的旧结果；必须指定任务，任务不在目录里报引擎的说明。
func TestFactorTaskRecheckAsksTheEngine(t *testing.T) {
	gateway := &statusGateway{}
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, runFactorTaskRecheck(ctx, gateway, "ftask_a", false, &out))
	require.Contains(t, out.String(), "ftask_a：计算中", "引擎重查之后检查全部通过")
	require.ErrorContains(t, runFactorTaskRecheck(ctx, gateway, "", false, &out), "--task-id")
	require.ErrorContains(t, runFactorTaskRecheck(ctx, gateway, "ftask_x", false, &out), "不在引擎的目录里")
}
