package client

import (
	"context"
	"os"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type topologyInvoker func(context.Context, string, string, any, any) error

func (f topologyInvoker) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestSyncHostPlacementsSendsCompleteCanonicalInventoryWithoutSecrets(t *testing.T) {
	snapshot := clientSnapshot(t)
	host := snapshot.Manifest.ControlHost()
	snapshot.Manifest.Placements[host.Name] = []string{"web-host", "monitor", "console-proxy", "admin", "eventbus"}
	calls := 0
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.ops.SysDeploy", service)
		require.Equal(t, "SyncHostPlacements", method)
		request := req.(*pb.SyncHostPlacementsReq)
		require.Equal(t, host.Name, request.GetHostId())
		require.Equal(t, host.Address, request.GetAddress())
		require.Equal(t, []string{"admin", "console-proxy", "eventbus", "monitor", "web-host"}, request.GetComponentIds())
		raw, err := protojson.Marshal(request)
		require.NoError(t, err)
		require.NotContains(t, strings.ToLower(string(raw)), "password")
		require.NotContains(t, strings.ToLower(string(raw)), "secret")
		rsp.(*pb.SyncHostPlacementsRsp).RetInfo = &pb.RetInfo{}
		return nil
	})
	require.NoError(t, New(gateway).SyncHostPlacements(t.Context(), snapshot, host.Name))
	require.Equal(t, 1, calls)
	require.Error(t, New(gateway).SyncHostPlacements(t.Context(), snapshot, "unknown-host"))
	require.Equal(t, 1, calls)
}

func TestSyncHostPlacementsRejectsChangedInputAndMissingStatus(t *testing.T) {
	snapshot, path := clientSnapshotWithPath(t)
	host := snapshot.Manifest.ControlHost()
	gateway := topologyInvoker(func(_ context.Context, _, _ string, _, rsp any) error { return nil })
	require.ErrorContains(t, New(gateway).SyncHostPlacements(t.Context(), snapshot, host.Name), "setup_response_invalid")
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0600))
	require.ErrorContains(t, New(gateway).SyncHostPlacements(t.Context(), snapshot, host.Name), "config_changed")
}
