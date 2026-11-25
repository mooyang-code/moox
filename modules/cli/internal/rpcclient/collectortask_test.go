package rpcclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collectorTestClient(server *httptest.Server) *Client {
	return &Client{Gateway: testfixture.HandlerGateway{Handler: server.Config.Handler}}
}

func TestEnableCollectionTaskPreservesCanonicalDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		switch r.URL.Path {
		case "/api/admin/collector/GetTaskDetail":
			assert.Equal(t, "stockcn", body["space_id"])
			assert.Equal(t, "d5v5n3p8r7c9m2k4j6h1", body["task_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"task":{"space_id":"stockcn","task_id":"d5v5n3p8r7c9m2k4j6h1","task_name":"A 股 K 线 1m","data_type":"kline","tag_ids":["cn_a_share"],"enabled":false,"collect_params":{"frequency":"1m","target_dataset_id":"dataset_d5v5n3p8r7c9m2k4j6h1"}}}`))
		case "/api/admin/collector/UpdateTask":
			task, ok := body["task"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, true, task["enabled"])
			assert.Equal(t, "dataset_d5v5n3p8r7c9m2k4j6h1", task["collect_params"].(map[string]any)["target_dataset_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	require.NoError(t, collectorTestClient(server).EnableCollectionTask(context.Background(), "stockcn", "d5v5n3p8r7c9m2k4j6h1"))
}

func TestCreateTaskStartsDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/CreateTask", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		task, ok := body["task"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "stockcn", task["space_id"])
		assert.NotContains(t, task, "task_id")
		assert.Equal(t, "A 股 K 线 1m", task["task_name"])
		assert.Equal(t, false, task["enabled"])
		assert.Equal(t, []any{"cn_a_share"}, task["tag_ids"])
		assert.Equal(t, "1m", task["collect_params"].(map[string]any)["frequency"])
		assert.NotContains(t, task["collect_params"], "target_dataset_id")
		resultConfig, ok := body["result_config"].(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, resultConfig, "keep_duration")
		assert.Equal(t, "A 股 K 线结果", resultConfig["description"])
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"task_id":"d5v5n3p8r7c9m2k4j6h1"}`))
	}))
	defer server.Close()

	taskID, err := collectorTestClient(server).CreateTask(context.Background(), "stockcn", "A 股 K 线 1m", "kline", "moox-cli", []string{"cn_a_share"}, map[string]any{
		"frequency": "1m",
	}, &ResultConfig{Description: "A 股 K 线结果"})
	require.NoError(t, err)
	assert.Equal(t, "d5v5n3p8r7c9m2k4j6h1", taskID)
}

func TestCreateTaskRejectsUserSelectedResultDatasetID(t *testing.T) {
	_, err := (&Client{}).CreateTask(context.Background(), "stockcn", "任务一", "kline", "moox-cli", []string{"cn_a_share"}, map[string]any{
		"target_dataset_id": "dataset-user-selected",
		"frequency":         "1m",
	}, nil)
	require.ErrorContains(t, err, "target_dataset_id")
}

func TestListEnabledTasksDecodesTaskNameAndResultSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/admin/collector/GetTaskList", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, true, body["enabled"])
		assert.NotContains(t, body, "market_type")
		_, _ = w.Write([]byte(`{
			"ret_info":{"code":0,"msg":"ok"},
			"tasks":[{
				"space_id":"stockcn",
				"task_id":"builtin-stockcn-kline-1m",
				"task_name":"A 股 K 线 1m",
				"data_type":"kline",
				"tag_ids":["cn_a_share"],
				"enabled":true,
				"result":{
					"result_name":"采集结果",
					"dataset_id":"dataset-1",
					"status":"ready",
					"last_data_time":"2026-09-20T12:00:00Z",
					"data_kind":"time_series"
				}
			}],
			"page":{"has_more":false}
		}`))
	}))
	defer server.Close()

	tasks, err := collectorTestClient(server).ListEnabledTasks(context.Background(), "stockcn")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "A 股 K 线 1m", tasks[0].TaskName)
	assert.Equal(t, "dataset-1", tasks[0].Result.DatasetID)
	assert.Equal(t, "ready", tasks[0].Result.Status)
	assert.Equal(t, "2026-09-20T12:00:00Z", tasks[0].Result.LastDataTime)
	assert.Equal(t, "time_series", tasks[0].Result.DataKind)
}
