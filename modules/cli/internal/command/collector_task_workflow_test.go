package command

import (
	"context"
	"encoding/json"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureStockCNKlineTaskCreatesGeneratedTaskFromTagDefinition(t *testing.T) {
	generatedID := "d5v5n3p8r7c9m2k4j6h1"
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		switch r.URL.Path {
		case "/api/admin/collectmgr/GetTaskList":
			if !created {
				_, _ = w.Write([]byte(`{"ret_info":{"code":0},"tasks":[],"page":{"has_more":false}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"tasks":[{"space_id":"stockcn","task_id":"` + generatedID + `","task_name":"A 股 K 线 1m","data_type":"kline","tag_ids":["cn_a_share"],"enabled":true}],"page":{"has_more":false}}`))
		case "/api/admin/collectmgr/CreateTask":
			task, ok := body["task"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "stockcn", task["space_id"])
			assert.NotContains(t, task, "task_id")
			assert.Equal(t, "A 股 K 线 1m", task["task_name"])
			assert.Equal(t, []any{"cn_a_share"}, task["tag_ids"])
			collectParams := task["collect_params"].(map[string]any)
			assert.Equal(t, "1m", collectParams["frequency"])
			assert.NotContains(t, collectParams, "provider")
			assert.NotContains(t, collectParams, "market_type")
			created = true
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"task_id":"` + generatedID + `"}`))
		case "/api/admin/collectmgr/GetTaskDetail":
			assert.Equal(t, generatedID, body["task_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"task":{"space_id":"stockcn","task_id":"` + generatedID + `","task_name":"A 股 K 线 1m","data_type":"kline","tag_ids":["cn_a_share"],"enabled":false,"collect_params":{"frequency":"1m"}}}`))
		case "/api/admin/collectmgr/UpdateTask":
			task := body["task"].(map[string]any)
			assert.Equal(t, true, task["enabled"])
			assert.Equal(t, generatedID, task["task_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	taskID, err := ensureStockCNKlineTask(context.Background(), collectorTestClient(server), "stockcn")
	require.NoError(t, err)
	assert.Equal(t, generatedID, taskID)
}

func TestCollectorTaskWorkflowHelpUsesTaskTerminology(t *testing.T) {
	assert.NotContains(t, collectorFunctionActivateStockCNCmd.Short, "规则")
	flag := collectorFunctionPublishSubmitCmd.Flags().Lookup("enable-stockcn")
	require.NotNil(t, flag)
	assert.NotContains(t, flag.Usage, "规则")
}

func collectorTestClient(server *httptest.Server) *adminclient.Client {
	return &adminclient.Client{Gateway: testfixture.HandlerGateway{Handler: server.Config.Handler}}
}

func newControlFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) {
		return testfixture.HandlerGateway{Handler: handler}, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })
	return server
}
