package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorType_Constants_ShouldMatchExpectedValues(t *testing.T) {
	assert.Equal(t, CollectorType("binance"), CollectorTypeBinance)
}

func TestEventAction_Constants_ShouldMatchExpectedValues(t *testing.T) {
	assert.Equal(t, EventAction("task"), EventActionTask)
	assert.Equal(t, EventAction("keepalive"), EventActionKeepalive)
}

func TestHeartbeatPayload_JSONRoundTrip_ShouldPreserveFields(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	payload := HeartbeatPayload{
		SpaceID:  "space-1",
		NodeID:   "node-1",
		NodeType: "collector",
		Timestamp: now,
		RunningTasks: []*TaskSummary{{ID: "task-1", Type: "kline", Status: "running"}},
		Metrics: &NodeMetrics{CPUUsage: 0.5, TaskCount: 1, Timestamp: now},
		SupportedCollectors: []string{"kline"},
		LocalDNSRecords: []*LocalDNSReportItem{{Domain: "api.binance.com", IPList: []string{"1.1.1.1"}, ResolveAt: now}},
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded HeartbeatPayload
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, payload.SpaceID, decoded.SpaceID)
	assert.Equal(t, payload.NodeID, decoded.NodeID)
	require.Len(t, decoded.RunningTasks, 1)
	assert.Equal(t, "task-1", decoded.RunningTasks[0].ID)
}

func TestCollectParams_AndCollectResult_ShouldMarshal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	params := CollectParams{Symbol: "BTCUSDT", Interval: "1m", StartTime: &start, Limit: 100}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "BTCUSDT")

	result := CollectResult{Count: 2, Timestamp: start, Metadata: map[string]interface{}{"source": "binance"}}
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"count":2`)
}

func TestCloudFunctionEvent_ServiceDeployment_ShouldMarshal(t *testing.T) {
	event := CloudFunctionEvent{
		Action: EventActionKeepalive,
		ServiceDeployments: map[string]ServiceDeployment{
			"collector": {ServiceName: "collector", Host: "127.0.0.1", Port: 8080, Status: "active"},
		},
	}
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "collector")
}
