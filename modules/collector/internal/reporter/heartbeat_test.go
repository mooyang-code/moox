package reporter

import (
	"context"
	"encoding/json"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetermineNodeState_PrefersCustom(t *testing.T) {
	assert.Equal(t, "custom", determineNodeState("custom", "running"))
	assert.Equal(t, "running", determineNodeState("", "running"))
}

func TestParseServerResponse_AcceptsSuccess(t *testing.T) {
	err := parseServerResponse([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	require.NoError(t, err)
}

func TestParseServerResponse_RejectsErrorCode(t *testing.T) {
	err := parseServerResponse([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestParseServerResponse_RejectsMissingRetInfo(t *testing.T) {
	err := parseServerResponse([]byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server returned error code")
}

func TestParseServerResponse_RejectsMalformedJSON(t *testing.T) {
	err := parseServerResponse([]byte("not-json"))
	require.Error(t, err)
}

func TestHandleControlDirective_DoesNotPanic(t *testing.T) {
	handleControlDirective(ControlDirective{Type: 2, JobItemID: "j1", AttemptNo: 1, Reason: "stop"})
	handleControlDirective(ControlDirective{Type: 99})
}

func TestBuildProbeResponse_WithNodeInfo(t *testing.T) {
	runtimeapp.UpdateNodeInfo("node-test", "v1")
	t.Cleanup(func() { runtimeapp.UpdateNodeInfo("", "") })

	resp, err := buildProbeResponse(func(opts *BuildProbeResponseOptions) {
		opts.CustomState = "ready"
		opts.IncludeTasks = true
	})
	require.NoError(t, err)
	assert.Equal(t, "node-test", resp.NodeID)
	assert.Equal(t, "ready", resp.State)
	assert.NotNil(t, resp.Details.NodeInfo)
	assert.Equal(t, "node-test", resp.Details.NodeInfo.NodeID)
}

func TestDefaultProbeResponseConfig(t *testing.T) {
	cfg := DefaultProbeResponseConfig()
	assert.Equal(t, "running", cfg.State)
	assert.Equal(t, "30s", cfg.Interval)
}

func TestResultMap_ParsesJSON(t *testing.T) {
	got := resultMap(`{"k":"v"}`)
	assert.Equal(t, "v", got["k"])
	assert.Equal(t, map[string]any{"message": "not-json"}, resultMap("not-json"))
}

func TestReportHeartbeat_SkipsWhenNodeIDEmpty(t *testing.T) {
	runtimeapp.UpdateNodeInfo("", "v1")
	t.Cleanup(func() { runtimeapp.UpdateNodeInfo("", "") })
	assert.NoError(t, ReportHeartbeat(context.Background()))
}

func TestProcessProbe_UpdatesGatewayAndReturnsResponse(t *testing.T) {
	runtimeapp.UpdateNodeInfo("node-probe", "v1")
	runtimeapp.UpdateServiceGatewayTarget("http://127.0.0.1:11000")
	t.Cleanup(func() {
		runtimeapp.UpdateNodeInfo("", "")
		runtimeapp.UpdateServiceGatewayTarget("")
	})

	rsp, err := ProcessProbe(context.Background(), model.CloudFunctionEvent{
		ServiceGatewayTarget: "http://127.0.0.1:11000",
	})
	require.NoError(t, err)
	assert.True(t, rsp.Success)
	assert.Contains(t, rsp.Message, "probe handled")
}

func TestSendSingleHeartbeat_Success(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("X-Moox-Target-Node = %q, want gateway-gz-122", got)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ServerResponse{RetInfo: &ServerRetInfo{Code: 0, Msg: "ok"}})
	}))
	defer server.Close()

	err := sendSingleHeartbeat(context.Background(), server.URL, []byte(`{}`), server.Client())
	require.NoError(t, err)
}

func TestCollectNodeMetrics_ReturnsMemoryUsage(t *testing.T) {
	metrics := collectNodeMetrics()
	assert.NotNil(t, metrics)
	assert.GreaterOrEqual(t, metrics.MemoryUsage, float64(0))
	assert.Equal(t, 0, metrics.TaskCount)
}

func TestBuildPayloadInfo_UsesRuntimeAndEnvSpace(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "space-test")
	runtimeapp.UpdateNodeInfo("node-payload", "v9")
	httpclient.Init()
	t.Cleanup(func() { runtimeapp.UpdateNodeInfo("", "") })

	payload, err := buildPayloadInfo()

	require.NoError(t, err)
	assert.Equal(t, "space-test", payload.SpaceID)
	assert.Equal(t, "node-payload", payload.NodeID)
	assert.Equal(t, "scf", payload.NodeType)
	assert.Equal(t, "v9", payload.Metadata["version"])
	assert.NotNil(t, payload.Metrics)
	assert.Nil(t, payload.LocalDNSRecords)
}

func TestSendToServer_RejectsEmptyGatewayTarget(t *testing.T) {
	err := sendToServer(context.Background(), &model.HeartbeatPayload{NodeID: "node-1"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid service gateway target")
}

func TestProbeResponseHelpers_ReturnDefaults(t *testing.T) {
	assert.Empty(t, getRunningTasks())
	assert.Equal(t, model.TaskStatsInfo{}, getTaskStatistics())

	info := getHeartbeatInfo(&ProbeResponseConfig{Interval: "15s", ReportCount: 2, ErrorCount: 1})
	assert.Equal(t, int64(2), info.ReportCount)
	assert.Equal(t, int64(1), info.ErrorCount)
	assert.Equal(t, "15s", info.Interval)
	assert.False(t, info.LastReport.IsZero())
}

func TestCreateNodeInfo_IncludesVersion(t *testing.T) {
	info := createNodeInfo("node-1", "v2")
	assert.Equal(t, "node-1", info.NodeID)
	assert.Equal(t, "v2", info.Version)
	assert.Equal(t, "scf", info.NodeType)
}
