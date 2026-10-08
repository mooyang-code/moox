package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectorPublishLeaseLifecycleAndFencePropagation(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trpc.moox.admin.CollectorPublishLease/AcquireCollectorPublishLease", "/trpc.moox.admin.CollectorPublishLease/RenewCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"space_id":"crypto","lease_id":"lease-1","fencing_token":"7","expires_at":"2026-10-03T12:02:00Z"}`))
		case "/trpc.moox.admin.CollectorPublishLease/ReleaseCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"},"released":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	lease, err := client.AcquireCollectorPublishLease(context.Background(), "crypto", "publisher-1")
	require.NoError(t, err)
	require.EqualValues(t, 7, lease.FencingToken)
	require.Equal(t, "lease-1", lease.LeaseID)

	client.SetCollectorPublishLease(lease)
	items := client.withPublishFenceToDeployItems([]NodeDeployItem{{NodeID: "node-1", PackageID: "pkg-1"}})
	require.Equal(t, "lease-1", items[0].CollectorPublishLeaseID)
	require.EqualValues(t, 7, items[0].CollectorPublishFencingToken)

	renewed, err := client.RenewCollectorPublishLease(context.Background(), lease)
	require.NoError(t, err)
	renewed.HolderID = lease.HolderID
	client.SetCollectorPublishLease(renewed)
	require.NoError(t, client.ReleaseCollectorPublishLease(context.Background(), renewed))
	require.Nil(t, client.CollectorPublishFence())
	require.Equal(t, []string{
		"/trpc.moox.admin.CollectorPublishLease/AcquireCollectorPublishLease",
		"/trpc.moox.admin.CollectorPublishLease/RenewCollectorPublishLease",
		"/trpc.moox.admin.CollectorPublishLease/ReleaseCollectorPublishLease",
	}, methods)
}

func TestSubmitDeleteNodesCarriesPublishFence(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/trpc.moox.cloudnode.CloudNodeMgr/SubmitDeleteNodes", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"ret_info\":{\"code\":0,\"msg\":\"ok\"},\"job_id\":\"job-1\",\"operation\":3,\"total_count\":1}"))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	client.SetCollectorPublishLease(&CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 17})
	response, err := client.SubmitDeleteNodes(context.Background(), []string{"node-1"})
	require.NoError(t, err)
	require.Equal(t, "job-1", response.JobID)
	require.Equal(t, "NODE_BATCH_OPERATION_DELETE_NODES", response.Operation)
	require.Equal(t, "lease-1", body["collector_publish_lease_id"])
	require.EqualValues(t, 17, body["collector_publish_fencing_token"])
}

func TestDecodeCollectorPublishLeaseRejectsInvalidFence(t *testing.T) {
	_, err := decodeCollectorPublishLease([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease","fencing_token":"0","expires_at":"2026-10-03T12:02:00Z"}`), "AcquireCollectorPublishLease")
	require.Error(t, err)
	_, err = decodeCollectorPublishLease([]byte(`{"ret_info":{"code":14,"msg":"lease held"}}`), "AcquireCollectorPublishLease")
	require.Error(t, err)
	_, err = decodeCollectorPublishLease([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease","fencing_token":7,"expires_at":"2026-10-03T12:02:00Z"}`), "AcquireCollectorPublishLease")
	require.NoError(t, err)
}
