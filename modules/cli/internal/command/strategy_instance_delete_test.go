package command

import (
	"bytes"
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/errs"
)

// deleteGateway 模拟策略与因子服务：实例 i1 停用中，现货任务还在、永续任务上次已经删掉。
type deleteGateway struct {
	methods   []string
	deleteErr error
	deleteRsp string
}

func (g *deleteGateway) Forward(_ context.Context, service, method string, _ int, _ []byte) ([]byte, error) {
	g.methods = append(g.methods, method)
	switch method {
	case "GetStrategyInstance":
		return []byte(`{"ret_info":{"code":0},"instance":{"instance_id":"i1","strategy_id":"s1","enabled":false,
			"factor_tasks":{"spot":"ftask_spot","swap":"ftask_swap","multi":"ftask_shared"}}}`), nil
	case "DeleteStrategyInstance":
		if g.deleteErr != nil {
			return nil, g.deleteErr
		}
		if g.deleteRsp != "" {
			return []byte(g.deleteRsp), nil
		}
		return []byte(`{"ret_info":{"code":0}}`), nil
	}
	return nil, errs.New(errs.RetServerNoFunc, service+"/"+method)
}

func (g *deleteGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	g.methods = append(g.methods, method)
	out := rsp.(*factorpb.GetFactorTaskRsp)
	switch req.(*factorpb.GetFactorTaskReq).GetTaskId() {
	case "ftask_swap":
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND}
		return nil
	case "ftask_shared":
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.FactorTask = &factorpb.FactorTask{TaskId: "ftask_shared", Name: "公共任务", ResultDatasetId: "dataset_factor_shared"}
		return nil
	}
	out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	out.FactorTask = &factorpb.FactorTask{TaskId: "ftask_spot", Name: "现货因子", ResultDatasetId: "dataset_factor_spot", BoundInstanceId: "i1"}
	return nil
}

func (g *deleteGateway) Close() error { return nil }

func runStrategyDeleteCommand(t *testing.T, gateway *deleteGateway, args ...string) (string, error) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
	cmd := newStrategyInstanceDeleteCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"--instance-id", "i1"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// 不带 --yes 只列出将要删除的任务和数据集，不发删除请求。
func TestStrategyInstanceDeleteOnlyPreviewsWithoutYes(t *testing.T) {
	gateway := &deleteGateway{}
	out, err := runStrategyDeleteCommand(t, gateway)
	require.NoError(t, err)
	preview := parsePreview(t, out)
	assert.Equal(t, "delete_strategy_instance", preview["action"])
	assert.Contains(t, preview["warning"], "不可恢复")
	tasks := preview["delete_factor_tasks"].([]any)
	require.Len(t, tasks, 2)
	assert.Equal(t, "dataset_factor_spot", tasks[0].(map[string]any)["result_dataset_id"])
	assert.Equal(t, "already_deleted", tasks[1].(map[string]any)["state"])
	kept := preview["keep_factor_tasks"].([]any)
	require.Len(t, kept, 1)
	assert.Equal(t, "ftask_shared", kept[0].(map[string]any)["task_id"])
	assert.Equal(t, "未绑定到此实例", kept[0].(map[string]any)["reason"])
	assert.NotContains(t, gateway.methods, "DeleteStrategyInstance")
}

func TestStrategyInstanceDeleteRunsWithYes(t *testing.T) {
	gateway := &deleteGateway{}
	out, err := runStrategyDeleteCommand(t, gateway, "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, `"status": "deleted"`)
	assert.Contains(t, gateway.methods, "DeleteStrategyInstance")
}

// 超时（结果未知）或服务端报超时：提示删除未完成、稍后重试。
func TestStrategyInstanceDeleteTimeoutAsksToRetry(t *testing.T) {
	_, err := runStrategyDeleteCommand(t, &deleteGateway{deleteErr: errs.New(errs.RetClientTimeout, "client timeout")}, "--yes")
	require.ErrorContains(t, err, strategyInstanceDeleteIncomplete)

	_, err = runStrategyDeleteCommand(t, &deleteGateway{deleteRsp: `{"ret_info":{"code":4,"msg":"删除因子任务 ftask_spot 未完成：请求超时"},"incomplete":true}`}, "--yes")
	require.ErrorContains(t, err, strategyInstanceDeleteIncomplete)

	// 还没动手就失败（例如读不到归档任务）：原样报错，不加“删除未完成”。
	_, err = runStrategyDeleteCommand(t, &deleteGateway{deleteRsp: `{"ret_info":{"code":4,"msg":"读不到归档任务，暂不能删除"}}`}, "--yes")
	require.ErrorContains(t, err, "读不到归档任务")
	assert.NotContains(t, err.Error(), strategyInstanceDeleteIncomplete)

	_, err = runStrategyDeleteCommand(t, &deleteGateway{deleteRsp: `{"ret_info":{"code":1,"msg":"结果数据集 x 有归档任务"}}`}, "--yes")
	require.ErrorContains(t, err, "归档任务")
	assert.NotContains(t, err.Error(), strategyInstanceDeleteIncomplete)
}
