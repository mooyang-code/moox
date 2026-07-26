package serverless

import (
	"context"
	"encoding/json"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"testing"
)

func TestHandleKeepaliveRunsPollWithServiceGatewayTarget(t *testing.T) {
	oldReport := reportHeartbeatAfterProbe
	oldPoll := pollJobItemsAfterHeartbeat
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReport
		pollJobItemsAfterHeartbeat = oldPoll
	})

	heartbeats := 0
	polls := 0
	reportHeartbeatAfterProbe = func(ctx context.Context) error {
		heartbeats++
		return nil
	}
	pollJobItemsAfterHeartbeat = func(ctx context.Context) error {
		polls++
		return nil
	}

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		RequestID:             "test-request",
		FunctionName:          "moox-collector-ap-guangzhou-0",
		FunctionVersion:       "$LATEST",
		Namespace:             "default",
		TencentcloudRegion:    "ap-guangzhou",
		InvokedFunctionUnique: "moox-collector-ap-guangzhou-0",
		TencentcloudAppID:     "test-app",
		TencentcloudUin:       "test-uin",
	})
	rsp, err := NewCloudFunctionHandler().handleKeepalive(ctx, model.CloudFunctionEvent{
		Action:               model.EventActionKeepalive,
		Source:               "collector_schedule",
		ServiceGatewayTarget: "http://127.0.0.1:11000",
	})
	if err != nil {
		t.Fatalf("handleKeepalive() error = %v", err)
	}
	if rsp == nil || !rsp.Success {
		t.Fatalf("handleKeepalive() response = %#v, want success", rsp)
	}
	if heartbeats != 1 {
		t.Fatalf("heartbeats = %d, want 1", heartbeats)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1", polls)
	}
}

func TestDeploymentHelpers_ShouldResolveURLsAndTargets(t *testing.T) {
	deployments := map[string]model.ServiceDeployment{
		"service_gateway": {
			ServiceName: "service_gateway", Protocol: "http", Host: "127.0.0.1", Port: 11000,
		},
		"storage_primary": {
			ServiceName: "storage_primary", Protocol: "trpc", Host: "10.0.0.1", Port: 20102,
		},
	}
	assert.Equal(t, "http://127.0.0.1:11000", deploymentBaseURL(deployments, "service_gateway"))
	assert.Contains(t, deploymentTRPCTarget(deployments, "storage_primary"), "10.0.0.1")
	assert.True(t, isHTTPProtocol("http"))
	assert.False(t, isHTTPProtocol("trpc"))
	assert.True(t, deploymentMatches(deployments["service_gateway"], "service_gateway"))
	assert.Equal(t, "service_gateway", normalizeDeploymentName(" Service_Gateway "))

	assert.True(t, isKeepaliveProbeSource("keepalive_probe"))
	assert.False(t, isKeepaliveProbeSource("manual"))
}

func TestCloudFunctionHandler_InvalidEvent(t *testing.T) {
	h := NewCloudFunctionHandler()
	rsp, err := h.HandleRequest(context.Background(), json.RawMessage(`not-json`))
	require.NoError(t, err)
	resp, ok := rsp.(*model.Response)
	require.True(t, ok)
	assert.False(t, resp.Success)
}

func TestErrorResponse_ShouldPopulateFields(t *testing.T) {
	h := NewCloudFunctionHandler()
	rsp := h.errorResponse("bad", "failed")
	assert.False(t, rsp.Success)
	assert.Equal(t, "failed", rsp.Message)
}

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
			"storage-primary": {
				ServiceName: "storage-primary", Protocol: "trpc", Host: "10.0.0.1", Port: 20100,
				RPCAddress: "10.0.0.1:20100",
			},
		},
		Data: map[string]any{"node_id": "node-scf-1"},
	}
	got := h.applyRuntimeConfig(withFunctionContext(context.Background()), event, &functioncontext.FunctionContext{
		FunctionName: "fallback-fn",
	})
	assert.Equal(t, "http://127.0.0.1:11000", got.ServiceGatewayTarget)
	assert.Equal(t, "10.0.0.1:20100", got.StorageRPCGatewayTarget)
	assert.Equal(t, "10.0.0.1:20100", got.StorageRPCGatewayTarget)
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
