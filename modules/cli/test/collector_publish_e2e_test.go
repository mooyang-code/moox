package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorPublishStatusQueriesRealJobAndItems(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		require.Equal(t, "/api/admin/cloudnode/GetNodeBatchChange", r.URL.Path)
		_, _ = w.Write([]byte(`{
			"ret_info":{"code":0},
			"job":{"job_id":"node-batch-e2e","status":"NODE_BATCH_STATUS_FAILED","total_count":1,"failed_count":1},
			"items":[{"item_id":"item-1","node_id":"node-1","status":"NODE_BATCH_ITEM_STATUS_FAILED","error_message":"SCF rejected"}]
		}`))
	}))
	defer server.Close()

	binary := buildMooxCLI(t)
	command := exec.Command(binary,
		"collector", "function", "publish", "status",
		"--control-url", server.URL,
		"--job-id", "node-batch-e2e",
	)
	output, err := command.Output()
	require.NoError(t, err)

	var response struct {
		Job struct {
			JobID  string `json:"job_id"`
			Status string `json:"status"`
		} `json:"job"`
		Items []struct {
			NodeID       string `json:"node_id"`
			ErrorMessage string `json:"error_message"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(output, &response))
	assert.Equal(t, "node-batch-e2e", response.Job.JobID)
	assert.Equal(t, "NODE_BATCH_STATUS_FAILED", response.Job.Status)
	require.Len(t, response.Items, 1)
	assert.Equal(t, "SCF rejected", response.Items[0].ErrorMessage)
	assert.Equal(t, 1, requests)
}

func TestCollectorPublishCommandsRejectPositionalArguments(t *testing.T) {
	binary := buildMooxCLI(t)
	cases := [][]string{
		{"collector", "function", "publish", "status", "node-batch-fake", "--control-url", "http://127.0.0.1:1", "--job-id", "node-batch-real"},
		{"collector", "function", "publish", "submit", "collector.zip"},
	}
	for _, args := range cases {
		command := exec.Command(binary, args...)
		output, err := command.CombinedOutput()
		require.Error(t, err, "command unexpectedly accepted positional args: %v\n%s", args, output)
		assert.Contains(t, string(output), "unknown command")
	}
}

func buildMooxCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "moox-cli")
	command := exec.Command("go", "build", "-o", binary, "../cmd/moox-cli")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build moox-cli: %s", output)
	return binary
}
