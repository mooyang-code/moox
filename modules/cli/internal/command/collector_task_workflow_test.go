package command

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureStockCNKlineTaskCreatesStableTaskWithoutResultDatasetID(t *testing.T) {
	detailCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		switch r.URL.Path {
		case "/api/admin/collectmgr/GetTaskDetail":
			detailCalls++
			if detailCalls == 1 {
				_, _ = w.Write([]byte(`{"ret_info":{"code":404,"msg":"record not found"}}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"ret_info":{"code":0},
				"task":{
					"space_id":"stockcn",
					"task_id":"builtin-stockcn-kline-1m",
					"task_name":"A 股 K 线 1m",
					"data_type":"kline",
					"provider":"stockcn_multi",
					"market_type":"equity",
					"enabled":false,
					"collect_params":{
						"provider":"stockcn_multi",
						"market_type":"equity",
						"subject_tags":["cn_a_share"],
						"frequency":"1m"
					}
				}
			}`))
		case "/api/admin/collectmgr/CreateTask":
			task, ok := body["task"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "stockcn", task["space_id"])
			assert.Equal(t, "builtin-stockcn-kline-1m", task["task_id"])
			assert.Equal(t, "A 股 K 线 1m", task["task_name"])
			assert.Equal(t, false, task["enabled"])
			collectParams, ok := task["collect_params"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, []any{"cn_a_share"}, collectParams["subject_tags"])
			assert.NotContains(t, collectParams, "target_dataset_id")
			assert.NotContains(t, body, "result_config")
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"task_id":"builtin-stockcn-kline-1m"}`))
		case "/api/admin/collectmgr/UpdateTask":
			task, ok := body["task"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, true, task["enabled"])
			assert.Equal(t, "A 股 K 线 1m", task["task_name"])
			assert.NotContains(t, task["collect_params"].(map[string]any), "target_dataset_id")
			_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	require.NoError(t, ensureStockCNKlineTask(context.Background(), adminclient.New(server.URL), "stockcn"))
}

func TestCollectorTaskWorkflowHelpUsesTaskTerminology(t *testing.T) {
	assert.NotContains(t, collectorFunctionActivateStockCNCmd.Short, "规则")
	flag := collectorFunctionPublishSubmitCmd.Flags().Lookup("enable-stockcn")
	require.NotNil(t, flag)
	assert.NotContains(t, flag.Usage, "规则")
}
