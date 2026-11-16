package command

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/errs"
)

// confirmGateway 模拟策略服务：实例 i1 当前的会话是 session-7。
type confirmGateway struct {
	methods  []string
	requests map[string]map[string]any
}

func (g *confirmGateway) Forward(_ context.Context, service, method string, _ int, body []byte) ([]byte, error) {
	g.methods = append(g.methods, method)
	var request map[string]any
	_ = json.Unmarshal(body, &request)
	if g.requests == nil {
		g.requests = map[string]map[string]any{}
	}
	g.requests[method] = request
	switch method {
	case "GetStrategyInstance":
		return []byte(`{"ret_info":{"code":0},"instance":{"instance_id":"i1","enabled":true,"session_id":"session-7","logical_account_id":"acc"}}`), nil
	case "ConfirmStrategyInstance":
		return []byte(`{"ret_info":{"code":0},"instance":{"instance_id":"i1","session_id":"session-7"}}`), nil
	}
	return nil, errs.New(errs.RetServerNoFunc, service+"/"+method)
}

func (g *confirmGateway) Invoke(context.Context, string, string, any, any) error {
	return errs.New(errs.RetServerNoFunc, "invoke")
}

func (g *confirmGateway) Close() error { return nil }

func runConfirmCommand(t *testing.T, gateway *confirmGateway, args ...string) string {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
	cmd := buildRPCCommand(specByPath(t, "strategy", "instance", "confirm"))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"--instance-id", "i1"}, args...))
	require.NoError(t, cmd.Execute())
	return out.String()
}

// 确认实盘不给 --session-id 时，预览里带上实例当前的会话号；执行时只确认这一次启用。给了就原样用，不再取回。
func TestStrategyInstanceConfirmPinsTheCurrentSession(t *testing.T) {
	gateway := &confirmGateway{}
	preview := parsePreview(t, runConfirmCommand(t, gateway))
	require.Equal(t, "session-7", preview["request"].(map[string]any)["session_id"], "预览里能看到要确认的会话")
	require.Equal(t, []string{"GetStrategyInstance"}, gateway.methods, "预览不发确认请求")

	gateway = &confirmGateway{}
	runConfirmCommand(t, gateway, "--yes")
	require.Equal(t, "session-7", gateway.requests["ConfirmStrategyInstance"]["session_id"])

	gateway = &confirmGateway{}
	runConfirmCommand(t, gateway, "--session-id", "session-3", "--yes")
	require.Equal(t, []string{"ConfirmStrategyInstance"}, gateway.methods, "给了会话号就不取回")
	require.Equal(t, "session-3", gateway.requests["ConfirmStrategyInstance"]["session_id"])
}
