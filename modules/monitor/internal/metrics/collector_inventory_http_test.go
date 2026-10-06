package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestCollectorInventoryHTTPClientCallsGatewayHTTPEntry(t *testing.T) {
	credentials := gatewayauth.Credentials{KeyID: "monitor", Secret: "test-gateway-secret"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/service/collectmgr/GetTaskResultInventory", r.URL.Path)
		require.Equal(t, "crypto", r.Header.Get("X-Space-Id"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		_, err = gatewayauth.Verify(credentials, gatewayauth.Request{Method: r.Method, Path: r.URL.Path, TargetNode: "control", Body: body}, r.Header, time.Now())
		require.NoError(t, err)
		req := &collectorpb.GetTaskResultInventoryReq{}
		require.NoError(t, protojson.Unmarshal(body, req))
		require.Equal(t, "crypto", req.GetSpaceId())
		_, _ = w.Write([]byte(`{"snapshotId":"snap-1","entries":[{"taskId":"task-1"}],"futureField":1}`))
	}))
	defer server.Close()

	c, err := NewCollectorInventoryHTTPClient(server.URL+"/", "control", credentials, "", 5*time.Second)
	require.NoError(t, err)
	rsp, err := c.GetTaskResultInventory(context.Background(), &collectorpb.GetTaskResultInventoryReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, "snap-1", rsp.GetSnapshotId())
	require.Len(t, rsp.GetEntries(), 1)
	require.Equal(t, "task-1", rsp.GetEntries()[0].GetTaskId())
}

func TestCollectorInventoryHTTPClientReportsGatewayRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "route not found", http.StatusNotFound)
	}))
	defer server.Close()

	c, err := NewCollectorInventoryHTTPClient(server.URL, "control", gatewayauth.Credentials{KeyID: "monitor", Secret: "secret"}, "", 5*time.Second)
	require.NoError(t, err)
	_, err = c.GetTaskResultInventory(context.Background(), &collectorpb.GetTaskResultInventoryReq{SpaceId: "crypto"})
	require.ErrorContains(t, err, "HTTP 404")
}
