package publishlease

import (
	"context"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// fakeLeaseAPI 记录调用并按方法返回预设响应。
type fakeLeaseAPI struct {
	calls    []string
	conflict string
}

func (f *fakeLeaseAPI) retInfo() *adminpb.RetInfo {
	if f.conflict != "" {
		return &adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: f.conflict}
	}
	return &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}
}

func (f *fakeLeaseAPI) ValidateCollectorPublishLease(_ context.Context, req *adminpb.ValidateCollectorPublishLeaseReq, _ ...client.Option) (*adminpb.ValidateCollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "validate")
	return &adminpb.ValidateCollectorPublishLeaseRsp{RetInfo: f.retInfo(), Valid: true, CurrentFencingToken: req.GetFencingToken()}, nil
}

func (f *fakeLeaseAPI) BeginCollectorPublishOperation(_ context.Context, req *adminpb.BeginCollectorPublishOperationReq, _ ...client.Option) (*adminpb.CollectorPublishOperationRsp, error) {
	f.calls = append(f.calls, "begin:"+req.GetSpaceId()+"/"+req.GetOperationId())
	return &adminpb.CollectorPublishOperationRsp{RetInfo: f.retInfo(), Active: f.conflict == ""}, nil
}

func (f *fakeLeaseAPI) RenewCollectorPublishOperation(_ context.Context, req *adminpb.CollectorPublishOperationReq, _ ...client.Option) (*adminpb.CollectorPublishOperationRsp, error) {
	f.calls = append(f.calls, "renew-operation")
	return &adminpb.CollectorPublishOperationRsp{RetInfo: f.retInfo(), Active: true}, nil
}

func (f *fakeLeaseAPI) EndCollectorPublishOperation(_ context.Context, _ *adminpb.CollectorPublishOperationReq, _ ...client.Option) (*adminpb.CollectorPublishOperationRsp, error) {
	f.calls = append(f.calls, "end-operation")
	return &adminpb.CollectorPublishOperationRsp{RetInfo: f.retInfo()}, nil
}

func (f *fakeLeaseAPI) AcquireCollectorPublishLease(_ context.Context, req *adminpb.AcquireCollectorPublishLeaseReq, _ ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "acquire:"+req.GetHolderId())
	if f.conflict != "" {
		return &adminpb.CollectorPublishLeaseRsp{RetInfo: f.retInfo()}, nil
	}
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: f.retInfo(), SpaceId: req.GetSpaceId(), LeaseId: "lease-new", FencingToken: req.GetExpectedFencingToken() + 1}, nil
}

func (f *fakeLeaseAPI) RenewCollectorPublishLease(_ context.Context, req *adminpb.RenewCollectorPublishLeaseReq, _ ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "renew-lease")
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: f.retInfo(), SpaceId: req.GetSpaceId(), LeaseId: req.GetLeaseId(), FencingToken: req.GetFencingToken()}, nil
}

func (f *fakeLeaseAPI) ReleaseCollectorPublishLease(_ context.Context, _ *adminpb.ReleaseCollectorPublishLeaseReq, _ ...client.Option) (*adminpb.ReleaseCollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "release")
	return &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: f.retInfo(), Released: true}, nil
}

func TestClientOperationClaimLifecycle(t *testing.T) {
	api := &fakeLeaseAPI{}
	c := &Client{api: api}
	ctx := context.Background()
	require.NoError(t, c.Validate(ctx, "crypto", "lease-1", 7))
	require.NoError(t, c.BeginOperation(ctx, "crypto", "lease-1", 7, "operation-1"))
	require.NoError(t, c.RenewOperation(ctx, "crypto", "operation-1", 7))
	require.NoError(t, c.EndOperation(ctx, "crypto", "operation-1", 7))
	require.Equal(t, []string{"validate", "begin:crypto/operation-1", "renew-operation", "end-operation"}, api.calls)
}

func TestClientRecoveryLeaseLifecycle(t *testing.T) {
	api := &fakeLeaseAPI{}
	c := &Client{api: api}
	lease, err := c.AcquireLease(context.Background(), "crypto", "cloudnode-recovery/job-1/item-1", 7)
	require.NoError(t, err)
	require.Equal(t, &Lease{SpaceID: "crypto", LeaseID: "lease-new", FencingToken: 8}, lease)
	require.NoError(t, c.RenewLease(context.Background(), lease))
	require.NoError(t, c.ReleaseLease(context.Background(), lease))
	require.Equal(t, []string{"acquire:cloudnode-recovery/job-1/item-1", "renew-lease", "release"}, api.calls)
}

func TestClientClassifiesStaleAndHeldLeaseResponses(t *testing.T) {
	_, err := (&Client{api: &fakeLeaseAPI{conflict: "collector publish lease is held"}}).AcquireLease(context.Background(), "crypto", "holder", 7)
	require.ErrorIs(t, err, ErrLeaseHeld)
	err = (&Client{api: &fakeLeaseAPI{conflict: "collector publish lease is expired or fenced"}}).BeginOperation(context.Background(), "crypto", "old-lease", 7, "operation-1")
	require.ErrorIs(t, err, ErrLeaseStale)
	require.ErrorIs(t, leaseResponseError(&adminpb.RetInfo{Code: adminpb.ErrorCode_CONFLICT, Msg: ErrLeaseSuperseded.Error()}), ErrLeaseSuperseded)
	require.Error(t, (*Client)(nil).Validate(context.Background(), "crypto", "lease", 1), "未配置的客户端应当报错")
}
