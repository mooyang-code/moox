package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 取回再合并：服务端的修改接口是整体替换，只给几个字段不能清空其余字段 ----

func specByPath(t *testing.T, path ...string) rpcSpec {
	t.Helper()
	for _, spec := range allRPCSpecs() {
		if len(spec.Path) == len(path) {
			same := true
			for i := range path {
				same = same && spec.Path[i] == path[i]
			}
			if same {
				return spec
			}
		}
	}
	t.Fatalf("没有命令 %v", path)
	return rpcSpec{}
}

func TestSpaceUpdateMergesExistingSpaceInsteadOfClearingFields(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"GetSpace":    `{"ret_info":{"code":0},"space":{"space_id":"crypto","name":"加密货币","description":"行情","owner":"quant","market":"crypto","timezone":"UTC","status":"disabled","attributes":{"k":"1"},"created_at":"1767225600000","updated_at":"1767312000000"}}`,
		"UpdateSpace": `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "space", "update"), "--space-id", "crypto", "--name", "新名")
	require.NoError(t, err)
	var get map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["GetSpace"], &get))
	assert.Equal(t, "crypto", get["space_id"])
	var request struct {
		Space map[string]any `json:"space"`
	}
	require.NoError(t, json.Unmarshal(gateway.bodies["UpdateSpace"], &request))
	assert.Equal(t, map[string]any{
		"space_id": "crypto", "name": "新名", "description": "行情", "owner": "quant", "market": "crypto",
		"timezone": "UTC", "status": "disabled", "attributes": map[string]any{"k": "1"},
	}, request.Space, "只改名称：其余字段原样带回，停用状态不能被重置成 active，时间戳不回传")
}

func TestCollectorTaskUpdateKeepsNameAndDescriptionWhenOnlyTogglingEnabled(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"GetTaskDetail": `{"ret_info":{"code":0},"task":{"space_id":"crypto","task_id":"t1","task_name":"合约 5m","description":"说明","enabled":true,"creator":"x","created_at":"1767225600000","definition_hash":"h"}}`,
		"UpdateTask":    `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "collector", "task", "update"), "--task-id", "t1", "--enabled=false")
	require.NoError(t, err)
	var request map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["UpdateTask"], &request))
	assert.Equal(t, "t1", request["task_id"])
	assert.Equal(t, map[string]any{"task_id": "t1", "task_name": "合约 5m", "description": "说明", "enabled": false}, request["task"],
		"服务端要求 task_name 非空且会覆盖说明：没给的字段要取回现值")
}

func TestDatasetUpdateMergesNameAndKeepsImmutableFieldsOut(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"GetDataset":    `{"ret_info":{"code":0},"dataset":{"space_id":"crypto","dataset_id":"d1","data_source_id":"binance","name":"现货 1m","description":"d","data_kind":"DATA_KIND_TIME_SERIES","frequency":"1m","status":"active","retention":"840h","revision":"3","attributes":{"a":"b"}}}`,
		"UpsertDataset": `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "metadata", "dataset", "update"), "--dataset-id", "d1", "--description", "新说明")
	require.NoError(t, err)
	var request struct {
		Dataset map[string]any `json:"dataset"`
	}
	require.NoError(t, json.Unmarshal(gateway.bodies["UpsertDataset"], &request))
	assert.Equal(t, "现货 1m", request.Dataset["name"], "服务端要求 name 非空：取回现值")
	assert.Equal(t, "新说明", request.Dataset["description"])
	for _, readOnly := range []string{"retention", "revision", "frequency", "data_kind"} {
		assert.NotContains(t, request.Dataset, readOnly, "%s 不能回传（只读或创建后不可变）", readOnly)
	}
	assert.NotContains(t, gateway.methods[len(gateway.methods)-1:], "GetDataset")
}

func TestMergeFailureStopsBeforeAnyWrite(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"GetTaskDetail": `{"ret_info":{"code":5,"msg":"记录不存在"}}`,
		"UpdateTask":    `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "collector", "task", "update"), "--task-id", "ghost", "--name", "x")
	require.ErrorContains(t, err, "取回现有对象失败")
	assert.NotContains(t, gateway.methods, "UpdateTask")
}

// ---- --data 里显式给的值不能被默认值覆盖 ----

func TestDataValuesSurviveDefaultSpaceAndPaging(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "list"}, Short: "t", Service: "trpc.moox.factor.FactorMgr", Method: "ListFactors", SpaceField: "space_id", Page: rpcPageSize}
	_, _, err := runSpecCommand(t, spec, "--data", `{"space_id":"stockcn","page":{"page":3,"size":7}}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"space_id":"stockcn","page":{"page":3,"size":7}}`, string(gateway.body), "没显式传参数时不能用默认值覆盖 --data")

	_, _, err = runSpecCommand(t, spec, "--data", `{"space_id":"stockcn"}`, "--space-id", "crypto", "--page-size", "9")
	require.NoError(t, err)
	assert.JSONEq(t, `{"space_id":"crypto","page":{"page":1,"size":9}}`, string(gateway.body), "显式传的参数照常优先")
}

func TestHostRequiredFlagRejectsMissingHost(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"agent_id":"a"}`}
	useRPCGateway(t, gateway)
	spec := specByPath(t, "host", "agent", "status")
	_, _, err := runSpecCommand(t, spec)
	require.ErrorContains(t, err, "缺少 --host")
	assert.Zero(t, gateway.calls, "不指定主机不能静默落到别的主机")
	_, _, err = runSpecCommand(t, spec, "--host", "control")
	require.NoError(t, err)
}

// ---- 预览：新增的几类命令不带 --yes 不发写请求 ----

func TestStateChangingSpecsOnlyPreviewWithoutYes(t *testing.T) {
	for _, path := range [][]string{
		{"strategy", "instance", "enable"}, {"strategy", "instance", "disable"}, {"collector", "task", "disable"},
		{"factor", "task", "disable"}, {"factor", "member", "disable"}, {"collector", "account", "delete"}, {"collector", "package", "delete"},
	} {
		spec := specByPath(t, path...)
		assert.True(t, spec.Destructive, "%v 应默认只预览", path)
	}
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	out, _, err := runSpecCommand(t, specByPath(t, "strategy", "instance", "enable"), "--instance-id", "i-1")
	require.NoError(t, err)
	assert.Zero(t, gateway.calls)
	assert.Equal(t, "SetStrategyInstanceEnabled", parsePreview(t, out)["method"])
}

func TestSystemPauseAndFetchPlanPauseOnlyPreviewWithoutYes(t *testing.T) {
	modules := newLifecycleGateway()
	modules.add(t, "collector", lifecycle.Hooks{})
	modules.add(t, "strategy", lifecycle.Hooks{})
	modules.use(t)
	pause, _, err := systemCmd.Find([]string{"pause"})
	require.NoError(t, err)
	var out, errOut bytes.Buffer
	pause.SetOut(&out)
	pause.SetErr(&errOut)
	pause.SetContext(context.Background())
	require.NoError(t, pause.RunE(pause, nil))
	preview := parsePreview(t, out.String())
	assert.Equal(t, "system_pause", preview["action"])
	assert.Equal(t, []any{"strategy", "collector"}, preview["modules"], "预览列出将要暂停的模块（下游到上游），没有部署的跳过")
	assert.Empty(t, modules.snapshot(), "预览不发暂停命令")

	gateway := &rpcFakeGateway{}
	useRPCGateway(t, gateway)
	out.Reset()
	fetchPlanPauseFlags.SpaceID, fetchPlanPauseYes = "crypto", false
	t.Cleanup(func() { fetchPlanPauseFlags.SpaceID = "" })
	collectorFetchPlanPauseCmd.SetOut(&out)
	collectorFetchPlanPauseCmd.SetErr(&errOut)
	collectorFetchPlanPauseCmd.SetContext(context.Background())
	require.NoError(t, collectorFetchPlanPauseCmd.RunE(collectorFetchPlanPauseCmd, nil))
	assert.Equal(t, "pause_fetch_plan", parsePreview(t, out.String())["action"])
	assert.Zero(t, gateway.calls)
}

// ---- gateway call：写方法默认预览，密钥写入被拒绝 ----

func TestGatewayCallWriteMethodsPreviewAndSecretWritesAreRefused(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	run := func(args ...string) (string, error) {
		cmd := newGatewayCallCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		cmd.SetContext(context.Background())
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("trpc.moox.strategy.StrategyMgr", "DeleteStrategy", "--data", `{"strategy_id":"x"}`)
	require.NoError(t, err)
	assert.Equal(t, "DeleteStrategy", parsePreview(t, out)["method"])
	assert.Zero(t, gateway.calls, "写方法不带 --yes 不能发出请求")

	_, err = run("trpc.moox.strategy.StrategyMgr", "DeleteStrategy", "--data", `{"strategy_id":"x"}`, "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, gateway.calls)

	_, err = run("trpc.moox.strategy.StrategyMgr", "ListStrategies")
	require.NoError(t, err)
	assert.Equal(t, 2, gateway.calls, "只读方法不受影响")

	for _, method := range []string{"CreateSecret", "UpdateSecret", "ToggleSecretStatus", "DeleteSecret"} {
		_, err = run("trpc.moox.ops.SecretMgr", method, "--yes")
		require.ErrorContains(t, err, "不允许调用", method)
	}
	assert.Equal(t, 2, gateway.calls)
}

// ---- host put：无法确认远端是否已存在时不能直接覆盖 ----

func TestHostPutRefusesToOverwriteWhenExistenceCannotBeConfirmed(t *testing.T) {
	fake := &hostFakeSSH{}
	useHostFake(t, fake)
	local := t.TempDir() + "/a.txt"
	require.NoError(t, os.WriteFile(local, []byte("hello"), 0o600))
	fake.runHook = func(argv []string) (setupssh.Result, error) {
		if argv[0] == "test" {
			return setupssh.Result{}, errors.New("ssh_command_failed: connection reset") // 退出码为 0 的传输错误
		}
		return setupssh.Result{}, nil
	}
	_, _, err := runHostCommand(t, "put", "--host", "storage", local, "/tmp/a.txt")
	require.ErrorContains(t, err, "无法确认远端文件")
	assert.Nil(t, fake.uploaded)
}

// ---- 命令层把 --yes 映射成预览（之前只在内部函数层测过） ----

func TestFunctionDeleteAndFirewallAndUdfPreviewAtCommandLevel(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"DeleteCloudFunctions": `{"ret_info":{"code":0},"results":[{"name":"f-1","region":"ap-nanjing","namespace":"default"}]}`,
	}}
	useRPCGateway(t, gateway)
	collectorFunctionDeleteFlags = collectorFunctionFlags{SpaceID: "crypto", Names: []string{"f-1"}}
	t.Cleanup(func() { collectorFunctionDeleteFlags = collectorFunctionFlags{} })
	var out bytes.Buffer
	collectorFunctionDeleteCmd.SetOut(&out)
	collectorFunctionDeleteCmd.SetErr(&bytes.Buffer{})
	collectorFunctionDeleteCmd.SetContext(context.Background())
	require.NoError(t, collectorFunctionDeleteCmd.RunE(collectorFunctionDeleteCmd, nil))
	assert.Equal(t, true, parsePreview(t, out.String())["preview"])
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["DeleteCloudFunctions"], &body))
	assert.Equal(t, true, body["dry_run"], "没有 --yes 只发预览，不删除")
}

func TestUdfActivateWithoutYesAsksServerForDryRunOnly(t *testing.T) {
	g := &udfFakeGateway{rsp: &factorpb.ActivateUdfRsp{RetInfo: udfOK(), Name: "ts_mean_udf", NewHash: testHash, DryRun: true}}
	useUdfGateway(t, g)
	activate, _, err := rootCmd.Find([]string{"udf", "activate"})
	require.NoError(t, err)
	activate.SetOut(&bytes.Buffer{})
	activate.SetErr(&bytes.Buffer{})
	activate.SetContext(context.Background())
	require.NoError(t, activate.RunE(activate, []string{"ts_mean_udf@" + testHash}))
	require.NotNil(t, g.req)
	assert.True(t, g.req.(*factorpb.ActivateUdfReq).GetDryRun(), "没有 --yes 时请求必须带 dry_run")
}

// 参数的默认值（如下单的市价、原因）不能覆盖 --data 里给的字段；没给时才用默认值，显式传参照常优先。
func TestFlagDefaultsDoNotOverrideData(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "submit"}, Short: "t", Service: "trpc.moox.trade.TradeConsoleService", Method: "SubmitOrder",
		Flags: []rpcFlag{{Name: "order-type", Field: "order_type", Kind: rpcString, Default: "ORDER_TYPE_MARKET"}, {Name: "reason", Field: "reason", Kind: rpcString, Default: "默认原因"}}}
	_, _, err := runSpecCommand(t, spec, "--data", `{"order_type":"ORDER_TYPE_LIMIT","reason":"原始原因"}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"order_type":"ORDER_TYPE_LIMIT","reason":"原始原因"}`, string(gateway.body))

	_, _, err = runSpecCommand(t, spec, "--data", `{"order_type":"ORDER_TYPE_LIMIT"}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"order_type":"ORDER_TYPE_LIMIT","reason":"默认原因"}`, string(gateway.body), "--data 没给的字段用默认值")

	_, _, err = runSpecCommand(t, spec, "--data", `{"order_type":"ORDER_TYPE_LIMIT"}`, "--order-type", "ORDER_TYPE_MARKET")
	require.NoError(t, err)
	assert.JSONEq(t, `{"order_type":"ORDER_TYPE_MARKET","reason":"默认原因"}`, string(gateway.body), "显式传的参数照常优先")
}

func TestMonitorArrivalTaskIDFlagDescribesFactorTask(t *testing.T) {
	spec := specByPath(t, "monitor", "arrival")
	cmd := buildRPCCommand(spec)
	flag := cmd.Flags().Lookup("task-id")
	require.NotNil(t, flag)
	assert.Contains(t, flag.Usage, "因子任务 ID")
	assert.NotContains(t, flag.Usage, "采集任务")
}
