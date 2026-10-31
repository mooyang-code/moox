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
	"github.com/spf13/cobra"
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
		"ListSpaces": `{"ret_info":{"code":0},"spaces":[
			{"id":"1","space_id":"stockcn","name":"A 股"},
			{"id":"2","space_id":"crypto","name":"加密货币","description":"行情","owner":"quant","market":"crypto","timezone":"UTC","status":"disabled","attributes":"{\"k\":1}","created_at":"2026-01-01"}],
			"page_result":{"has_more":false}}`,
		"UpdateSpace": `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "space", "update"), "--space-id", "crypto", "--name", "新名")
	require.NoError(t, err)
	var request struct {
		Space map[string]any `json:"space"`
	}
	require.NoError(t, json.Unmarshal(gateway.bodies["UpdateSpace"], &request))
	assert.Equal(t, map[string]any{
		"space_id": "crypto", "name": "新名", "description": "行情", "owner": "quant", "market": "crypto",
		"timezone": "UTC", "status": "disabled", "attributes": `{"k":1}`,
	}, request.Space, "只改名称：其余字段原样带回，停用状态不能被重置成 active，id 与时间戳不回传")

	_, _, err = runSpecCommand(t, specByPath(t, "space", "update"), "--space-id", "nowhere", "--name", "x")
	require.ErrorContains(t, err, "不存在")
}

func TestCollectorTaskUpdateKeepsNameAndDescriptionWhenOnlyTogglingEnabled(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"GetTaskDetail": `{"ret_info":{"code":0},"task":{"space_id":"crypto","task_id":"t1","task_name":"合约 5m","description":"说明","enabled":true,"creator":"x","create_time":"t","definition_hash":"h"}}`,
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
		"GetDataset":    `{"ret_info":{"code":0},"dataset":{"space_id":"crypto","dataset_id":"d1","data_source_id":"binance","name":"现货 1m","description":"d","data_kind":"DATA_KIND_TIME_SERIES","freq":"1m","status":"active","data_node_id":"storage-node-0","retention":"840h","revision":"3","attributes":{"a":"b"}}}`,
		"UpdateDataset": `{"ret_info":{"code":0}}`,
	}}
	useRPCGateway(t, gateway)
	_, _, err := runSpecCommand(t, specByPath(t, "metadata", "dataset", "update"), "--dataset-id", "d1", "--description", "新说明")
	require.NoError(t, err)
	var request struct {
		Dataset map[string]any `json:"dataset"`
	}
	require.NoError(t, json.Unmarshal(gateway.bodies["UpdateDataset"], &request))
	assert.Equal(t, "现货 1m", request.Dataset["name"], "服务端要求 name 非空：取回现值")
	assert.Equal(t, "新说明", request.Dataset["description"])
	for _, readOnly := range []string{"retention", "revision", "data_node_id", "freq", "data_kind"} {
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
		{"factor", "task", "disable"}, {"factor", "member", "disable"}, {"cloud", "function", "invoke"},
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
	gateway := &rpcFakeGateway{}
	useRPCGateway(t, gateway)
	systemFlags = systemOptions{Collector: "http://127.0.0.1:1", Strategy: "http://127.0.0.1:2"}
	t.Cleanup(func() { systemFlags = systemOptions{} })
	pause, _, err := systemCmd.Find([]string{"pause"})
	require.NoError(t, err)
	var out, errOut bytes.Buffer
	pause.SetOut(&out)
	pause.SetErr(&errOut)
	pause.SetContext(context.Background())
	require.NoError(t, pause.RunE(pause, nil))
	preview := parsePreview(t, out.String())
	assert.Equal(t, "system_pause", preview["action"])
	assert.Equal(t, []any{"strategy", "collector"}, preview["modules"], "预览列出将要暂停的模块（下游到上游）")
	assert.Zero(t, gateway.calls)

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

// ---- 多主机部署与回放缺模块 ----

func TestResolveRejectsComponentDeployedOnSeveralHosts(t *testing.T) {
	clearHealthEnv(t)
	remote := healthTestRemote(t, &healthFilesSSH{})
	remote.manifest.Deployments["factor"] = append(remote.manifest.Deployments["factor"], "strategy")
	_, err := resolveModuleHealthWith(context.Background(), remote, moduleHealthRequest{Modules: []string{"strategy"}})
	require.ErrorContains(t, err, "部署在多台主机上（control、factor）")
}

func TestReplayRequiresEveryModuleAndSurvivesEmptyArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "replay"}
	args, err := prepareReplayEndpoints(cmd, []string{"--file", "x.toml"})
	require.NoError(t, err, "只有 --file 没有子命令时不能 panic")
	assert.Empty(t, args)

	useFakeHealth(t, map[string]string{"factor-engine": "http://127.0.0.1:1", "strategy": "http://127.0.0.1:2"}, "trade")
	_, err = prepareReplayEndpoints(cmd, []string{"start", "--instance-id", "i"})
	require.ErrorContains(t, err, "没有部署 trade", "缺模块不能退回默认的本机地址")
}

// ---- 命令层把 --yes 映射成预览（之前只在内部函数层测过） ----

func TestFunctionDeleteAndFirewallAndUdfPreviewAtCommandLevel(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{
		"ListFunctionInstances":        `{"ret_info":{"code":0},"items":[{"instance_id":"f-1","function_type":"scf-event","workload_type":"market_fetcher","namespace":"default"}]}`,
		"AcquireCollectorPublishLease": leaseResponse,
	}}
	useRPCGateway(t, gateway)
	collectorDeleteFlags = collectorDeleteOptions{SpaceID: "crypto", Wait: true}
	t.Cleanup(func() { collectorDeleteFlags = collectorDeleteOptions{} })
	var out bytes.Buffer
	collectorFunctionDeleteCmd.SetOut(&out)
	collectorFunctionDeleteCmd.SetErr(&bytes.Buffer{})
	collectorFunctionDeleteCmd.SetContext(context.Background())
	require.NoError(t, collectorFunctionDeleteCmd.RunE(collectorFunctionDeleteCmd, nil))
	assert.Contains(t, out.String(), `"dry_run": true`)
	assert.NotContains(t, gateway.methods, "AcquireCollectorPublishLease", "没有 --yes 不能获取租约更不能提交删除")
	assert.NotContains(t, gateway.methods, "SubmitDeleteFunctionInstances")

	var fwOut bytes.Buffer
	lighthouseFirewallAddFlags = lighthouseFirewallAddOptions{InstanceID: "lhins-1", Ports: "20201", Protocol: "TCP", Cidr: "0.0.0.0/0", Action: "ACCEPT"}
	t.Cleanup(func() { lighthouseFirewallAddFlags = lighthouseFirewallAddOptions{} })
	lighthouseFirewallAddCmd.SetOut(&fwOut)
	lighthouseFirewallAddCmd.SetErr(&bytes.Buffer{})
	lighthouseFirewallAddCmd.SetContext(context.Background())
	require.NoError(t, lighthouseFirewallAddCmd.RunE(lighthouseFirewallAddCmd, nil))
	assert.Equal(t, "CreateFirewallRules", parsePreview(t, fwOut.String())["action"], "没有 --yes 只预览，不调用腾讯云")
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
