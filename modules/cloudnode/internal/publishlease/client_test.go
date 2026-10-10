package publishlease

import (
	"context"
	"errors"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type gatewayFunc func(context.Context, string, string, any, any) error

func (f gatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

func TestClientNativeLeaseAndOperationLifecycle(t *testing.T) {
	var methods []string
	client := New(gatewayFunc(func(ctx context.Context, service, method string, request, response any) error {
		require.Equal(t, "trpc.moox.admin.CollectorPublishLease", service)
		require.Equal(t, "crypto", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
		methods = append(methods, method)
		switch method {
		case "ValidateCollectorPublishLease":
			require.EqualValues(t, 7, request.(*adminpb.ValidateCollectorPublishLeaseReq).GetFencingToken())
			proto.Merge(response.(proto.Message), &adminpb.ValidateCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Valid: true, CurrentFencingToken: 7})
		case "BeginCollectorPublishOperation":
			require.Equal(t, "operation-1", request.(*adminpb.BeginCollectorPublishOperationReq).GetOperationId())
			proto.Merge(response.(proto.Message), &adminpb.CollectorPublishOperationRsp{RetInfo: &adminpb.RetInfo{}, Active: true})
		case "RenewCollectorPublishOperation", "EndCollectorPublishOperation":
			require.Equal(t, "operation-1", request.(*adminpb.CollectorPublishOperationReq).GetOperationId())
			proto.Merge(response.(proto.Message), &adminpb.CollectorPublishOperationRsp{RetInfo: &adminpb.RetInfo{}, Active: method != "EndCollectorPublishOperation"})
		case "AcquireCollectorPublishLease":
			require.Equal(t, "cloudnode-recovery/job-1/item-1", request.(*adminpb.AcquireCollectorPublishLeaseReq).GetHolderId())
			require.EqualValues(t, 7, request.(*adminpb.AcquireCollectorPublishLeaseReq).GetExpectedFencingToken())
			proto.Merge(response.(proto.Message), &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: "crypto", LeaseId: "lease-new", FencingToken: 8})
		case "RenewCollectorPublishLease":
			require.EqualValues(t, 8, request.(*adminpb.RenewCollectorPublishLeaseReq).GetFencingToken())
			proto.Merge(response.(proto.Message), &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: "crypto", LeaseId: "lease-new", FencingToken: 8})
		case "ReleaseCollectorPublishLease":
			require.EqualValues(t, 8, request.(*adminpb.ReleaseCollectorPublishLeaseReq).GetFencingToken())
			proto.Merge(response.(proto.Message), &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Released: true})
		default:
			t.Fatalf("unexpected method %s", method)
		}
		return nil
	}))
	require.NoError(t, client.Validate(t.Context(), "crypto", "lease-1", 7))
	require.NoError(t, client.BeginOperation(t.Context(), "crypto", "lease-1", 7, "operation-1"))
	require.NoError(t, client.RenewOperation(t.Context(), "crypto", "operation-1", 7))
	require.NoError(t, client.EndOperation(t.Context(), "crypto", "operation-1", 7))
	lease, err := client.AcquireLease(t.Context(), "crypto", "cloudnode-recovery/job-1/item-1", 7)
	require.NoError(t, err)
	require.Equal(t, &Lease{SpaceID: "crypto", LeaseID: "lease-new", FencingToken: 8}, lease)
	require.NoError(t, client.RenewLease(t.Context(), lease))
	require.NoError(t, client.ReleaseLease(t.Context(), lease))
	require.Len(t, methods, 7)
}

func TestClientClassifiesStaleAndHeldLeaseResponses(t *testing.T) {
	client := New(gatewayFunc(func(_ context.Context, _, method string, _, response any) error {
		if method == "AcquireCollectorPublishLease" {
			response.(*adminpb.CollectorPublishLeaseRsp).RetInfo = &adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: ErrLeaseHeld.Error()}
		} else {
			response.(*adminpb.CollectorPublishOperationRsp).RetInfo = &adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: ErrLeaseStale.Error()}
		}
		return nil
	}))
	_, err := client.AcquireLease(t.Context(), "crypto", "holder", 7)
	require.ErrorIs(t, err, ErrLeaseHeld)
	require.ErrorIs(t, client.BeginOperation(t.Context(), "crypto", "old-lease", 7, "operation-1"), ErrLeaseStale)
	require.ErrorIs(t, leaseResponseError(&adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: ErrLeaseSuperseded.Error()}), ErrLeaseSuperseded)
}

func TestClientDoesNotResendMutationOnLostResponse(t *testing.T) {
	calls := 0
	lost := errors.New("response lost")
	client := New(gatewayFunc(func(context.Context, string, string, any, any) error { calls++; return lost }))
	require.ErrorIs(t, client.BeginOperation(t.Context(), "crypto", "lease-1", 7, "operation-1"), lost)
	require.Equal(t, 1, calls)
	require.Error(t, New(nil).EndOperation(t.Context(), "crypto", "operation-1", 7))
}
