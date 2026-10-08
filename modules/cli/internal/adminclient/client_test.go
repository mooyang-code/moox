package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient 把调用发往 httptest 服务器：POST /<tRPC 服务名>/<方法>，空间 ID 放在 X-Space-Id 请求头。
// 与 admintest.Sender 相同；本包的测试不能引用 admintest（会形成循环引用）。
func newTestClient(baseURL string) *Client {
	return NewWithSender(func(ctx context.Context, servicePath, method string, body []byte, spaceID string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+servicePath+"/"+method, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if spaceID != "" {
			request.Header.Set("X-Space-Id", spaceID)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %s", response.Status)
		}
		return raw, nil
	})
}

func TestCallSendsServiceMethodBodyAndSpace(t *testing.T) {
	var gotService, gotMethod, gotSpace string
	var gotBody []byte
	client := NewWithSender(func(_ context.Context, servicePath, method string, body []byte, spaceID string) ([]byte, error) {
		gotService, gotMethod, gotBody, gotSpace = servicePath, method, body, spaceID
		return []byte(`{"ret_info":{"code":0}}`), nil
	})
	client.SpaceID = " crypto "
	var response struct {
		RetInfo retInfo `json:"ret_info"`
	}
	require.NoError(t, client.CallJSON(context.Background(), ServiceCloudNodeMgr, "ListCloudAccounts", map[string]string{"provider": "tencent"}, &response))
	assert.Equal(t, "trpc.moox.cloudnode.CloudNodeMgr", gotService)
	assert.Equal(t, "ListCloudAccounts", gotMethod)
	assert.JSONEq(t, `{"provider":"tencent"}`, string(gotBody))
	assert.Equal(t, "crypto", gotSpace)

	_, err := client.call(context.Background(), ServiceCloudNodeMgr, "GetNodeList", nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(gotBody), "没有请求体时发送空对象")
}

func TestCallReportsTransportFailuresAndEmptyResponses(t *testing.T) {
	failing := NewWithSender(func(context.Context, string, string, []byte, string) ([]byte, error) {
		return nil, errors.New("没有已启用的部署")
	})
	_, err := failing.call(context.Background(), ServiceCollectMgr, "GetTaskList", nil)
	require.ErrorContains(t, err, "trpc.moox.collector.CollectMgr/GetTaskList")
	empty := NewWithSender(func(context.Context, string, string, []byte, string) ([]byte, error) { return nil, nil })
	_, err = empty.call(context.Background(), ServiceCollectMgr, "GetTaskList", nil)
	require.ErrorContains(t, err, "空响应")
	_, err = (*Client)(nil).call(context.Background(), ServiceCollectMgr, "GetTaskList", nil)
	require.Error(t, err)
}

func TestIsRetInfoSuccess(t *testing.T) {
	assert.True(t, isRetInfoSuccess(0))
	assert.False(t, isRetInfoSuccess(1))
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
		case "/trpc.moox.cloudnode.CloudNodeMgr/SubmitCreateNodes":
			require.Len(t, body["nodes"], 1)
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job_id":"node-batch-1","operation":1,"total_count":1}`))
		case "/trpc.moox.cloudnode.CloudNodeMgr/GetNodeBatchChange":
			require.Equal(t, "node-batch-1", body["job_id"])
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"node-batch-1","operation":1,"status":4,"total_count":1,"failed_count":1},"items":[{"item_id":"item-1","node_id":"node-1","status":4,"error_message":"deploy failed"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL)
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
		"/trpc.moox.cloudnode.CloudNodeMgr/SubmitCreateNodes",
		"/trpc.moox.cloudnode.CloudNodeMgr/GetNodeBatchChange",
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
			_, err := newTestClient(server.URL).SubmitDeployNodes(context.Background(), []NodeDeployItem{{NodeID: "node-1", PackageID: "pkg-1"}})
			require.Error(t, err)
		})
	}
}

func TestGetNodeBatchChangeRequiresJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"items":[]}`))
	}))
	defer server.Close()
	_, err := newTestClient(server.URL).GetNodeBatchChange(context.Background(), "node-batch-1")
	require.ErrorContains(t, err, "empty job")
}

func TestGetNodeBatchChangeAcceptsRuntimeConfigOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":0},"job":{"job_id":"runtime-batch-1","operation":4,"status":3,"total_count":1},"items":[]}`))
	}))
	defer server.Close()

	status, err := newTestClient(server.URL).GetNodeBatchChange(context.Background(), "runtime-batch-1")
	require.NoError(t, err)
	require.NotNil(t, status.Job)
	assert.Equal(t, "NODE_BATCH_OPERATION_UPDATE_RUNTIME_CONFIGS", status.Job.Operation)
}
