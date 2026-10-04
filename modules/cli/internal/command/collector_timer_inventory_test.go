package command

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectCollectorTimerInventoryRequiresFreshScopedReadback(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldNow := collectorTimerInventoryNow
	collectorTimerInventoryNow = func() time.Time { return now }
	t.Cleanup(func() { collectorTimerInventoryNow = oldNow })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "crypto", r.Header.Get("X-Space-Id"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "account-a", body["cloud_account_id"])
		assert.Equal(t, "default", body["namespace"])
		assert.Equal(t, "ap-singapore", body["region"])
		assert.Equal(t, "scf-event", body["node_type"])
		assert.Equal(t, "timer", body["trigger_type"])
		if body["biz_type"] == "market_fetcher" {
			assert.Equal(t, float64(500), body["page"].(map[string]any)["size"])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret_info": map[string]any{"code": 0},
				"items": []any{map[string]any{
					"node_id": "timer-1", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore",
					"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
					"metadata": map[string]any{"timer_enabled": true, "timer_cron": "0 * * * * * *"},
				}},
				"page": map[string]any{"page": 1, "size": 500, "total": 1, "has_more": false},
			})
			return
		}
		assert.Empty(t, body["biz_type"], "provider readback must omit biz_type")
		assert.Equal(t, "timer-1", body["node_id"], "provider readback must target a catalog node")
		page := body["page"].(map[string]any)
		assert.Equal(t, float64(1), page["size"], "each readback response must contain at most one timer")
		pageNumber := int(page["page"].(float64))
		item := map[string]any{"node_id": "prefix-timer-1-suffix", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore", "node_type": "scf-event", "trigger_type": "timer", "biz_type": "other", "function_name": "other-timer", "metadata": map[string]any{}}
		if pageNumber == 2 {
			item = map[string]any{
				"node_id": "timer-1", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore",
				"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
				"metadata": map[string]any{
					"timer_enabled": true, "timer_cron": "0 * * * * * *",
					"timer_actual_enabled": true, "timer_actual_cron": "0 * * * * * *",
					"timer_actual_type": "timer", "timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1",
					"timer_available_status": "Available", "timer_last_readback_at": now.Format(time.RFC3339Nano),
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0}, "items": []any{item},
			"page": map[string]any{"page": pageNumber, "size": 1, "total": 2, "has_more": pageNumber == 1},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.NoError(t, err)
	assert.True(t, got.Complete)
	assert.Equal(t, 1, got.TimerCount)
	require.Len(t, got.Nodes, 1)
	assert.True(t, got.Nodes[0].ReadbackFresh)
	assert.Equal(t, "0 * * * * * *", got.Nodes[0].ActualCron)
}

func TestInspectCollectorTimerInventoryRejectsCachedReadbackFromBeforeStart(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldNow := collectorTimerInventoryNow
	collectorTimerInventoryNow = func() time.Time { return now }
	t.Cleanup(func() { collectorTimerInventoryNow = oldNow })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		metadata := map[string]any{
			"timer_enabled": true, "timer_cron": "0 * * * * * *",
			"timer_actual_enabled": true, "timer_actual_cron": "0 * * * * * *",
			"timer_actual_type": "timer", "timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1",
			"timer_available_status": "Available", "timer_last_readback_at": now.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			"timer_status_error": "secret provider detail",
		}
		if body["biz_type"] == "market_fetcher" {
			metadata = map[string]any{"timer_enabled": true, "timer_cron": "0 * * * * * *"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []any{map[string]any{
				"node_id": "timer-1", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore",
				"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
				"metadata": metadata,
			}},
			"page": map[string]any{"page": 1, "size": 4, "total": 1, "has_more": false},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.Error(t, err)
	assert.False(t, got.Complete)
	require.Len(t, got.Nodes, 1)
	assert.False(t, got.Nodes[0].ReadbackFresh)
	assert.True(t, got.Nodes[0].StatusErrorPresent)
	encoded, marshalErr := json.Marshal(got)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(encoded), "secret provider detail")
}

func TestInspectCollectorTimerInventoryRejectsDesiredActualDrift(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldNow := collectorTimerInventoryNow
	collectorTimerInventoryNow = func() time.Time { return now }
	t.Cleanup(func() { collectorTimerInventoryNow = oldNow })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		metadata := map[string]any{
			"timer_enabled": true, "timer_cron": "0 * * * * * *",
			"timer_actual_enabled": false, "timer_actual_cron": "0 * * * * * *",
			"timer_actual_type": "timer", "timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1",
			"timer_available_status": "Available", "timer_last_readback_at": now.Format(time.RFC3339Nano),
		}
		if body["biz_type"] == "market_fetcher" {
			metadata = map[string]any{"timer_enabled": true, "timer_cron": "0 * * * * * *"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []any{map[string]any{
				"node_id": "timer-1", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore",
				"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
				"metadata": metadata,
			}},
			"page": map[string]any{"page": 1, "size": 4, "total": 1, "has_more": false},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.Error(t, err)
	assert.False(t, got.Complete)
	require.Len(t, got.Nodes, 1)
	require.NotNil(t, got.Nodes[0].ActualEnabled)
	assert.False(t, *got.Nodes[0].ActualEnabled)
}

func TestInspectCollectorTimerInventoryRejectsTriggerContractDrift(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldNow := collectorTimerInventoryNow
	collectorTimerInventoryNow = func() time.Time { return now }
	t.Cleanup(func() { collectorTimerInventoryNow = oldNow })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		metadata := map[string]any{
			"timer_enabled": true, "timer_cron": "0 * * * * * *",
			"timer_actual_enabled": true, "timer_actual_cron": "0 * * * * * *",
			"timer_actual_type": "invoke", "timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1",
			"timer_available_status": "Available", "timer_last_readback_at": now.Format(time.RFC3339Nano),
		}
		if body["biz_type"] == "market_fetcher" {
			metadata = map[string]any{"timer_enabled": true, "timer_cron": "0 * * * * * *"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []any{map[string]any{
				"node_id": "timer-1", "cloud_account_id": "account-a", "namespace": "default", "region": "ap-singapore",
				"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
				"metadata": metadata,
			}},
			"page": map[string]any{"page": 1, "size": 1, "total": 1, "has_more": false},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.Error(t, err)
	assert.False(t, got.Complete)
	require.Len(t, got.Nodes, 1)
	assert.False(t, got.Nodes[0].ActualTypeMatch)
}

func TestReadCollectorTimerNodesBoundsConcurrentFuzzyPagination(t *testing.T) {
	targets := make([]adminclient.CloudNode, 0, 8)
	for _, nodeID := range []string{"node-1", "node-10", "node-11", "node-2", "node-20", "node-3", "node-30", "node-4"} {
		targets = append(targets, adminclient.CloudNode{
			NodeID: nodeID, CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
			NodeType: "scf-event", TriggerType: "timer", BizType: "market_fetcher",
		})
	}
	var activeRequests atomic.Int32
	var maxActiveRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := activeRequests.Add(1)
		defer activeRequests.Add(-1)
		for observed := maxActiveRequests.Load(); current > observed; observed = maxActiveRequests.Load() {
			if maxActiveRequests.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Empty(t, body["biz_type"])
		assert.Equal(t, float64(1), body["page"].(map[string]any)["size"], "LIKE pagination must refresh one node per server request")
		needle := body["node_id"].(string)
		page := int(body["page"].(map[string]any)["page"].(float64))
		matches := make([]adminclient.CloudNode, 0)
		for _, target := range targets {
			if strings.Contains(target.NodeID, needle) {
				target.Metadata = map[string]any{"timer_actual_type": "timer", "timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1"}
				matches = append(matches, target)
			}
		}
		var items []adminclient.CloudNode
		if page <= len(matches) {
			items = []adminclient.CloudNode{matches[page-1]}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0}, "items": items,
			"page": map[string]any{"page": page, "size": 1, "total": len(matches), "has_more": page < len(matches)},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	results := readCollectorTimerNodes(context.Background(), client, collectorTimerInventoryOptions{
		CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	}, targets)
	assert.LessOrEqual(t, maxActiveRequests.Load(), int32(collectorTimerInventoryReadbackWorkers))
	require.Len(t, results, len(targets))
	for index, result := range results {
		assert.True(t, result.found, targets[index].NodeID)
		assert.Equal(t, targets[index].NodeID, result.node.NodeID)
	}
}

func TestTimerInventoryAvailableRequiresProviderAvailableStatus(t *testing.T) {
	for _, test := range []struct {
		status string
		want   bool
	}{
		{status: "Available", want: true},
		{status: "available", want: true},
		{status: "Updating", want: false},
		{status: "Unknown", want: false},
		{status: "", want: false},
	} {
		t.Run(test.status, func(t *testing.T) {
			assert.Equal(t, test.want, timerInventoryAvailable(test.status))
		})
	}
}

func TestInspectCollectorTimerInventoryRejectsEmptyScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "market_fetcher", body["biz_type"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items":    []any{},
			"page":     map[string]any{"page": 1, "size": 4, "total": 0, "has_more": false},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.Error(t, err)
	assert.False(t, got.Complete)
	assert.Zero(t, got.TimerCount)
}

func TestInspectCollectorTimerInventoryFailsClosedOnScopeMismatch(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldNow := collectorTimerInventoryNow
	collectorTimerInventoryNow = func() time.Time { return now }
	t.Cleanup(func() { collectorTimerInventoryNow = oldNow })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "market_fetcher", body["biz_type"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0},
			"items": []any{map[string]any{
				"node_id": "unexpected-node", "cloud_account_id": "other-account", "namespace": "default", "region": "ap-singapore",
				"node_type": "scf-event", "trigger_type": "timer", "biz_type": "market_fetcher", "function_name": "market-timer-1",
				"metadata": map[string]any{
					"timer_enabled": true, "timer_cron": "0 * * * * * *",
					"timer_actual_enabled": true, "timer_actual_cron": "0 * * * * * *",
					"timer_available_status": "Available", "timer_last_readback_at": now.Format(time.RFC3339Nano),
				},
			}},
			"page": map[string]any{"page": 1, "size": 4, "total": 1, "has_more": false},
		})
	}))
	defer server.Close()

	client := adminclient.New(server.URL)
	client.SpaceID = "crypto"
	got, err := inspectCollectorTimerInventory(context.Background(), client, collectorTimerInventoryOptions{
		SpaceID: "crypto", CloudAccountID: "account-a", Namespace: "default", Region: "ap-singapore",
	})
	require.Error(t, err)
	assert.False(t, got.Complete)
	assert.Equal(t, 1, got.IntegrityIssueCount)
	assert.Empty(t, got.Nodes, "out-of-scope node details must not be echoed")
}
