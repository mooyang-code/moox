package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rpcFakeGateway 记录最后一次 Forward 并返回预置响应。
type rpcFakeGateway struct {
	service, method string
	body            []byte
	response        string
	// responses 按方法名给出响应，没有登记的方法用 response。
	responses map[string]string
	// bodies 记录每个方法最后一次请求体，methods 记录调用顺序。
	bodies  map[string][]byte
	methods []string
	// invokes 记录 pb 方式调用（Invoke）的方法名。
	invokes []string
	calls   int
	closed  bool
}

func (g *rpcFakeGateway) Forward(_ context.Context, service, method string, _ int, body []byte) ([]byte, error) {
	g.calls++
	g.service, g.method, g.body = service, method, body
	g.methods = append(g.methods, method)
	if g.bodies == nil {
		g.bodies = map[string][]byte{}
	}
	g.bodies[method] = body
	if response, ok := g.responses[method]; ok {
		return []byte(response), nil
	}
	return []byte(g.response), nil
}
func (g *rpcFakeGateway) Invoke(_ context.Context, _, method string, _, _ any) error {
	g.invokes = append(g.invokes, method)
	return nil
}
func (g *rpcFakeGateway) Close() error { g.closed = true; return nil }

func useRPCGateway(t *testing.T, gateway *rpcFakeGateway) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
}

func runSpecCommand(t *testing.T, spec rpcSpec, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := buildRPCCommand(spec)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestRPCRetError(t *testing.T) {
	for _, ok := range []string{
		`{"ret_info":{"code":0,"msg":"success"}}`,
		`{"ret_info":{"code":"0"}}`,
		`{"ret_info":{"code":"SUCCESS"}}`,
		`{"ret_info":{}}`,
		`{"agent_id":"x"}`,
		`not json`,
	} {
		assert.NoError(t, rpcRetError([]byte(ok)), ok)
	}
	err := rpcRetError([]byte(`{"ret_info":{"code":5,"msg":"记录不存在"}}`))
	var business *rpcBusinessError
	require.True(t, errors.As(err, &business))
	assert.Equal(t, "5", business.Code)
	assert.Contains(t, err.Error(), "记录不存在")
	require.Error(t, rpcRetError([]byte(`{"ret_info":{"code":"INVALID_PARAM","msg":"bad"}}`)))
}

func TestGatewayCallRefusesSecretPlaintextWithoutConnecting(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	for _, target := range [][2]string{
		{"trpc.moox.ops.SecretMgr", "GetSecretValue"},
		{"trpc.moox.monitor.MonitorMgr", "ListNotificationChannels"},
	} {
		cmd := newGatewayCallCommand()
		cmd.SetArgs([]string{target[0], target[1]})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetContext(context.Background())
		err := cmd.Execute()
		require.ErrorContains(t, err, "不允许调用", target[1])
	}
	assert.Zero(t, gateway.calls, "被拒绝的方法不能发出任何请求")
}

func TestGatewayCallBusinessFailurePrintsResponseAndFails(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":5,"msg":"记录不存在"},"strategy":null}`}
	useRPCGateway(t, gateway)
	cmd := newGatewayCallCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"trpc.moox.strategy.StrategyMgr", "GetStrategy", "--data", `{"strategy_id":"x"}`})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, out.String(), "记录不存在", "失败时响应照常输出")
	assert.True(t, gateway.closed)
}

func TestGatewayCallSuccessWithoutRetInfo(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"agent_id":"a"}`}
	useRPCGateway(t, gateway)
	cmd := newGatewayCallCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"trpc.moox.hostagent.HostAgentMgr", "GetStatus"})
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Execute())
	assert.JSONEq(t, `{"agent_id":"a"}`, out.String())
}

func TestSpecBuildsRequestFromFlagsDataAndPaging(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0},"items":[]}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{
		Path: []string{"x", "list"}, Short: "测试", Service: "trpc.moox.factor.FactorMgr", Method: "ListFactors",
		SpaceField: "space_id", Page: rpcPageSize,
		Flags: []rpcFlag{
			{Name: "task-id", Field: "task_id", Kind: rpcString},
			{Name: "include-spec", Field: "include_spec", Kind: rpcBool},
			{Name: "since", Field: "filter.since", Kind: rpcTime},
			{Name: "member", Field: "members", Kind: rpcStrings},
		},
	}
	dataFile := filepath.Join(t.TempDir(), "req.json")
	require.NoError(t, os.WriteFile(dataFile, []byte(`{"status":"enabled","task_id":"from-data"}`), 0o600))
	out, _, err := runSpecCommand(t, spec, "--space-id", "stockcn", "--task-id", "t1", "--include-spec", "--since", "2026-10-01T00:00:00Z",
		"--member", "a,b", "--page", "2", "--page-size", "10", "--data", "@"+dataFile)
	require.NoError(t, err)
	var request map[string]any
	require.NoError(t, json.Unmarshal(gateway.body, &request))
	assert.Equal(t, map[string]any{
		"space_id": "stockcn", "task_id": "t1", "status": "enabled", "include_spec": true,
		"filter": map[string]any{"since": "2026-10-01T00:00:00Z"}, "members": []any{"a", "b"},
		"page": map[string]any{"page": float64(2), "size": float64(10)},
	}, request, "参数优先于 --data")
	assert.Equal(t, "trpc.moox.factor.FactorMgr", gateway.service)
	assert.Equal(t, "ListFactors", gateway.method)
	assert.Contains(t, out, `"items"`)
}

func TestSpecPagingStyleForStrategy(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"s", "list"}, Short: "t", Service: "trpc.moox.strategy.StrategyMgr", Method: "ListStrategies", NoSpace: true, Page: rpcPagePSize}
	_, _, err := runSpecCommand(t, spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"page":{"page":1,"page_size":50}}`, string(gateway.body))
}

func TestSpecRequiredFlagMayComeFromData(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "show"}, Short: "t", Service: "trpc.moox.factor.FactorMgr", Method: "GetFactor", NoSpace: true,
		Flags: []rpcFlag{{Name: "factor-id", Field: "factor_id", Kind: rpcString, Required: true}}}
	_, _, err := runSpecCommand(t, spec)
	require.ErrorContains(t, err, "缺少 --factor-id")
	assert.Zero(t, gateway.calls)
	_, _, err = runSpecCommand(t, spec, "--data", `{"factor_id":"ma"}`)
	require.NoError(t, err)
	assert.JSONEq(t, `{"factor_id":"ma"}`, string(gateway.body))
}

func TestSpecDestructiveOnlyPreviewsWithoutYes(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "delete"}, Short: "t", Service: "trpc.moox.factor.FactorMgr", Method: "DeleteFactor", NoSpace: true, Destructive: true,
		Flags: []rpcFlag{{Name: "factor-id", Field: "factor_id", Kind: rpcString, Required: true}}}
	out, errOut, err := runSpecCommand(t, spec, "--factor-id", "ma")
	require.NoError(t, err)
	assert.Zero(t, gateway.calls, "没有 --yes 时不能发出请求")
	assert.Contains(t, errOut, "--yes")
	var preview map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &preview))
	assert.Equal(t, true, preview["preview"])
	assert.Equal(t, "DeleteFactor", preview["method"])
	assert.Equal(t, map[string]any{"factor_id": "ma"}, preview["request"])

	_, _, err = runSpecCommand(t, spec, "--factor-id", "ma", "--yes")
	require.NoError(t, err)
	assert.Equal(t, 1, gateway.calls)
	assert.JSONEq(t, `{"factor_id":"ma"}`, string(gateway.body))
}

func TestSpecAutoIDsNumberMapAndTimeValidation(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "recalc"}, Short: "t", Service: "trpc.moox.factor.FactorMgr", Method: "RecalcFactors", NoSpace: true,
		AutoIDs: []string{"request_id"},
		Flags: []rpcFlag{
			{Name: "param", Field: "params", Kind: rpcNumberMap},
			{Name: "from", Field: "start_time", Kind: rpcTime},
			{Name: "request-id", Field: "request_id", Kind: rpcString},
		}}
	_, _, err := runSpecCommand(t, spec, "--param", "window=20", "--param", "ratio=0.5")
	require.NoError(t, err)
	var request map[string]any
	require.NoError(t, json.Unmarshal(gateway.body, &request))
	assert.Equal(t, map[string]any{"window": float64(20), "ratio": 0.5}, request["params"])
	assert.Len(t, request["request_id"], 24, "缺省时自动生成请求 ID")

	_, _, err = runSpecCommand(t, spec, "--request-id", "mine")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(gateway.body, &request))
	assert.Equal(t, "mine", request["request_id"])

	_, _, err = runSpecCommand(t, spec, "--from", "yesterday")
	require.ErrorContains(t, err, "RFC3339")
	_, _, err = runSpecCommand(t, spec, "--param", "window=abc")
	require.ErrorContains(t, err, "需要数字")
}

func TestSpecBusinessFailureReturnsErrorAfterPrinting(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":5,"msg":"记录不存在"}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "show"}, Short: "t", Service: "trpc.moox.factor.FactorMgr", Method: "GetFactor", NoSpace: true}
	out, _, err := runSpecCommand(t, spec)
	require.Error(t, err)
	assert.Contains(t, out, "记录不存在")
}

func TestSpecFileFlagReadsContentAndStdin(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"ret_info":{"code":0}}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"x", "validate"}, Short: "t", Service: "trpc.moox.strategy.StrategyMgr", Method: "ValidateStrategy", NoSpace: true,
		Flags: []rpcFlag{{Name: "dsl-file", Field: "dsl_yaml", Kind: rpcFile, Required: true}}}
	path := filepath.Join(t.TempDir(), "s.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: a\n"), 0o600))
	_, _, err := runSpecCommand(t, spec, "--dsl-file", path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"dsl_yaml":"name: a\n"}`, string(gateway.body))
}

func TestSpecHostFlagIsPassedToInvocation(t *testing.T) {
	gateway := &rpcFakeGateway{response: `{"agent_id":"a"}`}
	useRPCGateway(t, gateway)
	spec := rpcSpec{Path: []string{"host", "agent", "status"}, Short: "t", Service: "trpc.moox.hostagent.HostAgentMgr", Method: "GetStatus", NoSpace: true, Host: true}
	cmd := buildRPCCommand(spec)
	assert.NotNil(t, cmd.Flags().Lookup("host"))
	assert.Nil(t, cmd.Flags().Lookup("space-id"))
	_, _, err := runSpecCommand(t, spec, "--host", "factor")
	require.NoError(t, err)
}

func TestMountRPCCommandsReusesExistingGroups(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	existing := &cobra.Command{Use: "storage", Short: "已有"}
	root.AddCommand(existing)
	group := findOrAddGroup(root, "storage", "storage")
	assert.Same(t, existing, group)
	assert.Panics(t, func() { findOrAddGroup(root, "nogroup", "nogroup") }, "新建命令组必须有说明")
	assert.True(t, strings.HasPrefix(buildRPCCommand(rpcSpec{Path: []string{"a"}, Short: "x", Service: "s", Method: "m"}).Long, "x"))
}
