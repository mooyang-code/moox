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

func withFunctionContext(ctx context.Context) context.Context {
	return functioncontext.NewContext(ctx, &functioncontext.FunctionContext{
		FunctionName:    "moox-collector-test",
		FunctionVersion: "v1",
		RequestID:       "req-1",
	})
}

func TestApplyRuntimeConfig_UpdatesFromDeployments(t *testing.T) {
	h := NewCloudFunctionHandler()
	event := model.CloudFunctionEvent{
		ServiceDeployments: map[string]model.ServiceDeployment{
			"service_gateway": {
				ServiceName: "service_gateway", Protocol: "http", Host: "127.0.0.1", Port: 11000,
				BaseURL: "http://127.0.0.1:11000",
			},
			"storage_access_trpc": {
				ServiceName: "storage_access_trpc", Protocol: "trpc", Host: "10.0.0.2", Port: 20102,
				RPCAddress: "10.0.0.2:20102",
			},
			"storage_metadata_trpc": {
				ServiceName: "storage_metadata_trpc", Protocol: "trpc", Host: "10.0.0.1", Port: 20100,
				RPCAddress: "10.0.0.1:20100",
			},
		},
		Data: map[string]any{"node_id": "node-scf-1"},
	}
	got := h.applyRuntimeConfig(withFunctionContext(context.Background()), event, &functioncontext.FunctionContext{
		FunctionName: "fallback-fn",
	})
	assert.Equal(t, "127.0.0.1", got.ServerIP)
	assert.Equal(t, 11000, got.ServerPort)
	assert.Equal(t, "10.0.0.1:20100", got.StorageMetadataTarget)
	assert.Equal(t, "10.0.0.2:20102", got.StorageAccessTarget)
	nodeID, _ := runtimeapp.GetNodeInfo()
	assert.Equal(t, "node-scf-1", nodeID)
}

func TestProcessCloudFunctionEvent_UnsupportedAndUnknown(t *testing.T) {
	h := NewCloudFunctionHandler()
	rsp, err := h.processCloudFunctionEvent(context.Background(), model.CloudFunctionEvent{Action: model.EventActionTask})
	require.NoError(t, err)
	assert.False(t, rsp.Success)
	assert.Contains(t, rsp.Message, "disabled")

	rsp, err = h.processCloudFunctionEvent(context.Background(), model.CloudFunctionEvent{Action: "unknown"})
	require.NoError(t, err)
	assert.False(t, rsp.Success)
	assert.Contains(t, rsp.Message, "unknown")
}

func TestHandleKeepalive_WithoutServerReturnsAlive(t *testing.T) {
	h := NewCloudFunctionHandler()
	rsp, err := h.handleKeepalive(withFunctionContext(context.Background()), model.CloudFunctionEvent{
		Action: model.EventActionKeepalive,
		Source: "manual",
	})
	require.NoError(t, err)
	assert.True(t, rsp.Success)
}

func TestHandleRequest_KeepaliveEvent(t *testing.T) {
	oldReport := reportHeartbeatAfterProbe
	oldPoll := pollJobItemsAfterHeartbeat
	reportHeartbeatAfterProbe = func(context.Context) error { return nil }
	pollJobItemsAfterHeartbeat = func(context.Context) error { return nil }
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReport
		pollJobItemsAfterHeartbeat = oldPoll
	})

	h := NewCloudFunctionHandler()
	raw, err := json.Marshal(model.CloudFunctionEvent{
		Action:               model.EventActionKeepalive,
		Source:               "keepalive_probe",
		ServiceGatewayTarget: "http://127.0.0.1:11000",
	})
	require.NoError(t, err)
	rsp, err := h.HandleRequest(withFunctionContext(context.Background()), raw)
	require.NoError(t, err)
	resp, ok := rsp.(*model.Response)
	require.True(t, ok)
	assert.True(t, resp.Success)
}

func TestDeploymentTRPCTargetValue_IPProtocol(t *testing.T) {
	got := deploymentTRPCTargetValue(model.ServiceDeployment{
		Protocol: "ip", Host: "127.0.0.1", Port: 20102,
	})
	assert.Equal(t, "ip://127.0.0.1:20102", got)
}
