package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRetInfoSuccess(t *testing.T) {
	assert.True(t, isRetInfoSuccess(0))
	assert.False(t, isRetInfoSuccess(1))
	assert.False(t, isRetInfoSuccess(200))
}

func TestResolvePackageType(t *testing.T) {
	assert.Equal(t, 1, ResolvePackageType("collector"))
	assert.Equal(t, 2, ResolvePackageType("factor"))
	assert.Equal(t, 1, ResolvePackageType("unknown"))
}

func TestSubmitCreateNodesAndGetNodeBatchChange(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		switch r.URL.Path {
		case "/api/admin/cloudnode/SubmitCreateNodes":
			require.Len(t, body["nodes"], 1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"node-batch-1","operation":1,"total_count":1}`))
		case "/api/admin/cloudnode/GetNodeBatchChange":
			require.Equal(t, "node-batch-1", body["job_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"node-batch-1","operation":1,"status":4,"total_count":1,"failed_count":1},"items":[{"item_id":"item-1","node_id":"node-1","status":4,"error_message":"deploy failed"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := collectorTestClient(server)
	submitted, err := client.SubmitCreateNodes(context.Background(), []NodeCreateItem{{PackageID: "pkg-1"}})
	require.NoError(t, err)
	assert.Equal(t, "node-batch-1", submitted.JobID)
	assert.Equal(t, "NODE_BATCH_OPERATION_CREATE_NODES", submitted.Operation)
	assert.Equal(t, 1, submitted.TotalCount)

	status, err := client.GetNodeBatchChange(context.Background(), submitted.JobID)
	require.NoError(t, err)
	assert.Equal(t, "node-batch-1", status.Job.JobID)
	assert.Equal(t, "NODE_BATCH_OPERATION_CREATE_NODES", status.Job.Operation)
	assert.Equal(t, "NODE_BATCH_STATUS_FAILED", status.Job.Status)
	require.Len(t, status.Items, 1)
	assert.Equal(t, "NODE_BATCH_ITEM_STATUS_FAILED", status.Items[0].Status)
	assert.Equal(t, "deploy failed", status.Items[0].ErrorMessage)
	assert.Equal(t, []string{
		"/api/admin/cloudnode/SubmitCreateNodes",
		"/api/admin/cloudnode/GetNodeBatchChange",
	}, paths)
}

func TestSubmitNodeBatchResponsesMustBeComplete(t *testing.T) {
	responses := []string{
		`{"job_id":"node-batch-1","total_count":1}`,
		`{"ret_info":{"code":1,"msg":"rejected"}}`,
		`{"ret_info":{"code":0},"job_id":"","total_count":1}`,
	}
	for _, response := range responses {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			_, err := collectorTestClient(server).SubmitDeployNodes(context.Background(), []NodeDeployItem{{NodeID: "node-1", PackageID: "pkg-1"}})
			require.Error(t, err)
		})
	}
}

func TestGetNodeBatchChangeRequiresJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[]}`))
	}))
	defer server.Close()
	_, err := collectorTestClient(server).GetNodeBatchChange(context.Background(), "node-batch-1")
	require.ErrorContains(t, err, "empty job")
}

func TestGetNodeBatchChangeAcceptsRuntimeConfigOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"runtime-batch-1","operation":4,"status":3,"total_count":1},"items":[]}`))
	}))
	defer server.Close()

	status, err := collectorTestClient(server).GetNodeBatchChange(context.Background(), "runtime-batch-1")
	require.NoError(t, err)
	require.NotNil(t, status.Job)
	assert.Equal(t, "NODE_BATCH_OPERATION_UPDATE_RUNTIME_CONFIGS", status.Job.Operation)
}
