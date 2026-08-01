package serverless

import (
	"context"
	"encoding/json"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tencentyun/scf-go-lib/functioncontext"
)

func TestDeploymentHelpersResolveURLsAndTargets(t *testing.T) {
	deployments := map[string]model.ServiceDeployment{
		"service_gateway": {ServiceName: "service_gateway", Protocol: "http", Host: "127.0.0.1", Port: 11000},
		"storage_primary": {ServiceName: "storage_primary", Protocol: "trpc", Host: "10.0.0.1", Port: 20102},
	}
	assert.Equal(t, "http://127.0.0.1:11000", deploymentBaseURL(deployments, "service_gateway"))
	assert.Contains(t, deploymentTRPCTarget(deployments, "storage_primary"), "10.0.0.1")
	assert.True(t, deploymentMatches(deployments["service_gateway"], "service_gateway"))
}

func TestCloudFunctionHandlerRejectsInvalidAndUnknownEvents(t *testing.T) {
	h := NewCloudFunctionHandler()
	rsp, err := h.HandleRequest(context.Background(), json.RawMessage(`not-json`))
	require.NoError(t, err)
	assert.False(t, rsp.(*model.Response).Success)
	unknown, err := h.processCloudFunctionEvent(context.Background(), model.CloudFunctionEvent{Action: "keepalive"})
	require.NoError(t, err)
	assert.False(t, unknown.Success)
}

func TestApplyRuntimeConfigUpdatesFromDeployments(t *testing.T) {
	h := NewCloudFunctionHandler()
	event := model.CloudFunctionEvent{ServiceDeployments: map[string]model.ServiceDeployment{
		"service_gateway": {ServiceName: "service_gateway", Protocol: "http", Host: "127.0.0.1", Port: 11000, BaseURL: "http://127.0.0.1:11000"},
		"storage-primary": {ServiceName: "storage-primary", Protocol: "trpc", Host: "10.0.0.1", Port: 20100, RPCAddress: "10.0.0.1:20100"},
	}, Data: map[string]any{"node_id": "node-scf-1"}}
	got := h.applyRuntimeConfig(withFunctionContext(context.Background()), event, &functioncontext.FunctionContext{FunctionName: "fallback-fn"})
	assert.Equal(t, "http://127.0.0.1:11000", got.ServiceGatewayTarget)
	assert.Equal(t, "10.0.0.1:20100", got.StorageRPCGatewayTarget)
	nodeID, _ := runtimeapp.GetNodeInfo()
	assert.Equal(t, "node-scf-1", nodeID)
}

func withFunctionContext(ctx context.Context) context.Context {
	return functioncontext.NewContext(ctx, &functioncontext.FunctionContext{FunctionName: "moox-collector-test", FunctionVersion: "v1", RequestID: "req-1"})
}
