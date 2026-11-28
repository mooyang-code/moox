package command

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cloudFunctionsFixture = `{"ret_info":{"code":0},"functions":[
	{"name":"moox-fetch-plan-crypto-spot-nanjing-000","region":"ap-nanjing","namespace":"default","kind":"timer","exists":true,"timer_matches":true,"package_latest":true,"environment_matches":true},
	{"name":"moox-fetch-plan-crypto-spot-nanjing-001","region":"ap-nanjing","namespace":"default","kind":"timer","exists":true,"timer_matches":false,"package_latest":true,"environment_matches":true},
	{"name":"moox-fetch-plan-crypto-spot-nanjing-001","region":"ap-nanjing","namespace":"legacy","kind":"timer","unplanned":true,"exists":true,"timer_matches":true,"package_latest":false},
	{"name":"moox-fetch-plan-crypto-spot-nanjing-002","region":"ap-nanjing","namespace":"default","kind":"timer","exists":false}]}`

func runCollectorFunctionCommand(t *testing.T, cmd *cobra.Command, run func() error) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	err := run()
	return out.String(), errOut.String(), err
}

// list：计划内与计划外分两组；--drift 只留不一致的计划内函数，计划外的照常列出并提示删除命令。
func TestCollectorFunctionListSeparatesUnplannedAndFiltersDrift(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{"ListCloudFunctions": cloudFunctionsFixture}}
	useRPCGateway(t, gateway)
	for drift, want := range map[bool][]string{
		false: {"moox-fetch-plan-crypto-spot-nanjing-000", "moox-fetch-plan-crypto-spot-nanjing-001", "moox-fetch-plan-crypto-spot-nanjing-002"},
		true:  {"moox-fetch-plan-crypto-spot-nanjing-001", "moox-fetch-plan-crypto-spot-nanjing-002"},
	} {
		var out bytes.Buffer
		require.NoError(t, runCollectorFunctionList(context.Background(), &out, collectorFunctionFlags{SpaceID: "crypto", Drift: drift}))
		var view collectorFunctionListView
		require.NoError(t, json.Unmarshal(out.Bytes(), &view))
		var names []string
		for _, function := range view.Functions {
			names = append(names, function.Name)
		}
		assert.Equal(t, want, names, "drift=%v", drift)
		require.Len(t, view.Unplanned, 1)
		assert.Equal(t, "legacy", view.Unplanned[0].Namespace)
		assert.Equal(t, 4, view.Total)
		assert.Equal(t, 3, view.DriftCount)
		assert.Equal(t, "moox-cli collector function delete --space-id crypto --name moox-fetch-plan-crypto-spot-nanjing-001 --namespace legacy --region ap-nanjing",
			view.Unplanned[0].DeleteCommand, "只删旧命名空间里的副本，不碰在用的同名函数")
		assert.Contains(t, view.Note, "delete_command")
	}
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["ListCloudFunctions"], &body))
	assert.Equal(t, "crypto", body["space_id"])
	require.ErrorContains(t, runCollectorFunctionList(context.Background(), &bytes.Buffer{}, collectorFunctionFlags{}), "--space-id")
	require.NoError(t, runCollectorFunctionList(context.Background(), &bytes.Buffer{}, collectorFunctionFlags{SpaceID: " Crypto "}))
	require.NoError(t, json.Unmarshal(gateway.bodies["ListCloudFunctions"], &body))
	assert.Equal(t, "crypto", body["space_id"], "空间 ID 统一成小写")
}

// delete 不加 --yes：发 dry_run 的 DeleteCloudFunctions，Collector 用与实际删除相同的匹配返回会删哪些；
// 命名空间与地域原样带上，被拒绝的单列；不读回、不删。
func TestCollectorFunctionDeletePreviewUsesTheServerDryRun(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{"DeleteCloudFunctions": `{"ret_info":{"code":0},"results":[
		{"name":"f-001","region":"ap-nanjing","namespace":"legacy"},
		{"name":"other-space-000","error":"函数 other-space-000 不属于空间 crypto"}]}`}}
	useRPCGateway(t, gateway)
	stdout, _, err := runCollectorFunctionCommand(t, collectorFunctionDeleteCmd, func() error {
		return runCollectorFunctionDelete(collectorFunctionDeleteCmd, collectorFunctionFlags{SpaceID: "crypto",
			Names: []string{"f-001", "other-space-000"}, Namespace: "legacy", Region: "AP-Nanjing"})
	})
	require.NoError(t, err)
	preview := parsePreview(t, stdout)
	targets := preview["targets"].([]any)
	require.Len(t, targets, 1)
	assert.Equal(t, "legacy", targets[0].(map[string]any)["namespace"])
	require.Len(t, preview["refused"].([]any), 1)
	assert.Contains(t, preview["note"], "collector function inspect", "提示旧账号下的函数怎么找")
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["DeleteCloudFunctions"], &body))
	assert.Equal(t, true, body["dry_run"])
	assert.Equal(t, "legacy", body["namespace"])
	assert.Equal(t, "ap-nanjing", body["region"])
	assert.Equal(t, []string{"DeleteCloudFunctions"}, gateway.methods, "预览只发一次 dry_run，不读回")

	_, _, err = runCollectorFunctionCommand(t, collectorFunctionDeleteCmd, func() error {
		return runCollectorFunctionDelete(collectorFunctionDeleteCmd, collectorFunctionFlags{SpaceID: "crypto"})
	})
	require.ErrorContains(t, err, "--name 与 --prefix")
}

// delete --yes：调 DeleteCloudFunctions，结果带命名空间；有失败的返回错误，还在删的给出查看命令。
func TestCollectorFunctionDeleteReportsResultsWithNamespaces(t *testing.T) {
	gateway := &rpcFakeGateway{responses: map[string]string{"DeleteCloudFunctions": `{"ret_info":{"code":0},"results":[
		{"name":"f-001","region":"ap-nanjing","namespace":"default","done":true},
		{"name":"f-001","region":"ap-nanjing","namespace":"legacy"},
		{"name":"f-009","error":"云上没有这个函数"}]}`}}
	useRPCGateway(t, gateway)
	stdout, _, err := runCollectorFunctionCommand(t, collectorFunctionDeleteCmd, func() error {
		return runCollectorFunctionDelete(collectorFunctionDeleteCmd, collectorFunctionFlags{SpaceID: "crypto", Names: []string{"f-001", "f-009"}, Yes: true})
	})
	require.ErrorContains(t, err, "1 个函数没删成")
	var summary struct {
		Results []rpcclient.CloudFunctionDeletion `json:"results"`
		Note    string                            `json:"note"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &summary))
	require.Len(t, summary.Results, 3)
	assert.Equal(t, "legacy", summary.Results[1].Namespace)
	assert.Contains(t, summary.Note, "1 个函数还在删")
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["DeleteCloudFunctions"], &body))
	assert.Equal(t, "crypto", body["space_id"])
	assert.Equal(t, []any{"f-001", "f-009"}, body["names"])
	assert.Equal(t, false, body["dry_run"], "--yes 真删")
	assert.NotContains(t, gateway.methods, "ListCloudFunctions", "--yes 直接删，不先读回")
}

// probe-egress：汇总不同的出口 IP；有探测失败的返回错误。
func TestCollectorFunctionProbeSummarisesDistinctEgressIPs(t *testing.T) {
	report := buildCollectorProbeReport("stockcn", []rpcclient.FunctionEgress{
		{Name: "a", IP: "198.51.100.2"}, {Name: "b", IP: "198.51.100.1"}, {Name: "c", IP: "198.51.100.2"}, {Name: "d", Error: "超时"},
	})
	assert.Equal(t, []string{"198.51.100.1", "198.51.100.2"}, report.DistinctOutboundIPs)
	assert.Equal(t, 2, report.DistinctCount)
	assert.Equal(t, 1, report.FailedCount)

	gateway := &rpcFakeGateway{responses: map[string]string{"ProbeFunctionEgress": `{"ret_info":{"code":0},"probes":[{"name":"a","ip":"198.51.100.1"},{"name":"b","error":"超时"}]}`}}
	useRPCGateway(t, gateway)
	var out bytes.Buffer
	err := runCollectorFunctionProbe(context.Background(), &out, collectorFunctionFlags{SpaceID: "stockcn", Names: []string{"a", "b"}})
	require.ErrorContains(t, err, "1 个函数探测失败")
	assert.Contains(t, out.String(), "198.51.100.1")
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["ProbeFunctionEgress"], &body))
	assert.Equal(t, []any{"a", "b"}, body["names"])
}

// 云账号与代码包命令都是全局规格命令：删除默认只预览，均不携带 space_id。
func TestCollectorAccountAndPackageCommandsPreviewDeletes(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	specs := map[string]rpcSpec{}
	for _, spec := range allRPCSpecs() {
		specs[spec.key()] = spec
	}
	stdout, _, err := runSpecCommand(t, specs[collectMgr+"/DeleteCloudAccount"], "--account-id", "old-account")
	require.NoError(t, err)
	assert.Equal(t, "old-account", parsePreview(t, stdout)["request"].(map[string]any)["account_id"])
	stdout, _, err = runSpecCommand(t, specs[collectMgr+"/DeleteFunctionPackage"], "--package-id", "pkg-1")
	require.NoError(t, err)
	deleteReq := parsePreview(t, stdout)["request"].(map[string]any)
	assert.Equal(t, "pkg-1", deleteReq["package_id"])
	assert.NotContains(t, deleteReq, "space_id")
	assert.Zero(t, gateway.calls, "预览不发请求")

	_, _, err = runSpecCommand(t, specs[collectMgr+"/ListFunctionPackages"])
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(gateway.bodies["ListFunctionPackages"], &body))
	assert.NotContains(t, body, "space_id")
	assert.Contains(t, specs[collectMgr+"/DeleteCloudAccount"].Short, "collector function inspect", "删账号的说明里写明旧账号下的函数怎么处理")
}
