package metrics

import (
	"context"
	"errors"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type inventoryInvoke func(context.Context, string, string, any, any) error

func (f inventoryInvoke) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestCollectorInventoryUsesSharedGatewayAndPreservesScope(t *testing.T) {
	request := &collectorpb.GetTaskResultInventoryReq{SpaceId: "crypto", SnapshotId: "snapshot-1"}
	wantErr := errors.New("gateway unavailable")
	var calls int
	gateway := inventoryInvoke(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.collector.CollectMgr", service)
		require.Equal(t, "GetTaskResultInventory", method)
		require.Same(t, request, req)
		if calls == 2 {
			return wantErr
		}
		*rsp.(*collectorpb.GetTaskResultInventoryRsp) = collectorpb.GetTaskResultInventoryRsp{SnapshotId: "snapshot-1"}
		return nil
	})
	c, err := NewCollectorInventoryGatewayClient(gateway)
	require.NoError(t, err)
	rsp, err := c.GetTaskResultInventory(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "snapshot-1", rsp.GetSnapshotId())
	_, err = c.GetTaskResultInventory(context.Background(), request)
	require.ErrorIs(t, err, wantErr)
	_, err = c.GetTaskResultInventory(context.Background(), request, client.WithTarget("ip://127.0.0.1:1"))
	require.ErrorContains(t, err, "overrides")
	require.Equal(t, 2, calls)
	_, err = NewCollectorInventoryGatewayClient(nil)
	require.Error(t, err)
}
