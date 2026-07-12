package serverless

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploymentHelpers_ShouldResolveURLsAndTargets(t *testing.T) {
	deployments := map[string]model.ServiceDeployment{
		"service_gateway": {
			ServiceName: "service_gateway", Protocol: "http", Host: "127.0.0.1", Port: 11000,
		},
		"storage_access": {
			ServiceName: "storage_access", Protocol: "trpc", Host: "10.0.0.1", Port: 20102,
		},
	}
	assert.Equal(t, "http://127.0.0.1:11000", deploymentBaseURL(deployments, "service_gateway"))
	assert.Contains(t, deploymentTRPCTarget(deployments, "storage_access"), "10.0.0.1")
	assert.True(t, isHTTPProtocol("http"))
	assert.False(t, isHTTPProtocol("trpc"))
	assert.True(t, deploymentMatches(deployments["service_gateway"], "service_gateway"))
	assert.Equal(t, "service_gateway", normalizeDeploymentName(" Service_Gateway "))

	host, port, ok := parseServerFromURL("http://127.0.0.1:11000")
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, 11000, port)
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
