package scfinvoker

import (
	"context"
	"errors"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/errs"
)

// spaceOf 取出调用选项里写入的 space 元数据。
func spaceOf(opts []client.Option) string {
	options := &client.Options{}
	for _, opt := range opts {
		opt(options)
	}
	return string(options.MetaData[gatewayroute.MetadataSpaceID])
}

type fakeCloudNode struct {
	spaces    []string
	submitRsp *cloudnodepb.SubmitNodeBatchRsp
	submitErr error
}

func (f *fakeCloudNode) GetNodeList(_ context.Context, _ *cloudnodepb.GetNodeListReq, opts ...client.Option) (*cloudnodepb.GetNodeListRsp, error) {
	f.spaces = append(f.spaces, spaceOf(opts))
	return &cloudnodepb.GetNodeListRsp{RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS}, Items: []*cloudnodepb.CloudNode{
		{NodeId: "ready", PackageId: "pkg", DeploymentId: "dep"},
		{NodeId: "package-only", PackageId: "pkg"},
	}}, nil
}

func (f *fakeCloudNode) SubmitUpdateNodeRuntimeConfigs(_ context.Context, _ *cloudnodepb.BatchUpdateNodeRuntimeConfigsReq, opts ...client.Option) (*cloudnodepb.SubmitNodeBatchRsp, error) {
	f.spaces = append(f.spaces, spaceOf(opts))
	return f.submitRsp, f.submitErr
}

func (f *fakeCloudNode) GetNodeBatchChange(context.Context, *cloudnodepb.GetNodeBatchChangeReq, ...client.Option) (*cloudnodepb.GetNodeBatchChangeRsp, error) {
	return &cloudnodepb.GetNodeBatchChangeRsp{RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS}, Job: &cloudnodepb.NodeBatchSummary{JobId: "job-1"}}, nil
}

func (f *fakeCloudNode) InvokeFunction(context.Context, *cloudnodepb.InvokeFunctionReq, ...client.Option) (*cloudnodepb.InvokeFunctionRsp, error) {
	return &cloudnodepb.InvokeFunctionRsp{RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS}, Scf: &cloudnodepb.ScfInvokeResult{RequestId: "req-1"}}, nil
}

type fakeLeases struct {
	calls    []string
	spaces   []string
	renewRsp *adminpb.CollectorPublishLeaseRsp
	renewErr error
}

func (f *fakeLeases) lease() *adminpb.CollectorPublishLeaseRsp {
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &commonpb.RetInfo{}, SpaceId: "crypto", LeaseId: "lease-1", FencingToken: 7, ExpiresAt: "2026-10-03T12:02:00Z"}
}

func (f *fakeLeases) AcquireCollectorPublishLease(_ context.Context, req *adminpb.AcquireCollectorPublishLeaseReq, opts ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "acquire:"+req.GetHolderId())
	f.spaces = append(f.spaces, spaceOf(opts))
	return f.lease(), nil
}

func (f *fakeLeases) RenewCollectorPublishLease(_ context.Context, _ *adminpb.RenewCollectorPublishLeaseReq, opts ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "renew")
	f.spaces = append(f.spaces, spaceOf(opts))
	if f.renewRsp != nil || f.renewErr != nil {
		return f.renewRsp, f.renewErr
	}
	return f.lease(), nil
}

func (f *fakeLeases) ReleaseCollectorPublishLease(_ context.Context, _ *adminpb.ReleaseCollectorPublishLeaseReq, opts ...client.Option) (*adminpb.ReleaseCollectorPublishLeaseRsp, error) {
	f.calls = append(f.calls, "release")
	f.spaces = append(f.spaces, spaceOf(opts))
	return &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &commonpb.RetInfo{}, Released: true}, nil
}

func TestIsDeployedRequiresPostDeployMarkerWhenDeploymentIDIsEmpty(t *testing.T) {
	if isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1"}) {
		t.Fatal("package-only node must not be invokable before deploy")
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", DeploymentId: "dep-1"}) {
		t.Fatal("node with deployment id should be invokable")
	}
	metadata, err := structpb.NewStruct(map[string]any{"deployment_ready": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", Metadata: metadata}) {
		t.Fatal("node with local deployment marker should be invokable")
	}
}

func TestListMarketFetchersSendsSpaceAndSkipsUndeployedNodes(t *testing.T) {
	cloudNode := &fakeCloudNode{}
	c := &Client{cloudnode: cloudNode, leases: &fakeLeases{}}
	nodes, err := c.ListMarketFetchers(context.Background(), "crypto")
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Equal(t, "ready", nodes[0].NodeID)
	require.Equal(t, []string{"crypto"}, cloudNode.spaces, "space 写入 tRPC 元数据")
}

func TestCollectorPublishLeaseLifecycle(t *testing.T) {
	leases := &fakeLeases{}
	c := &Client{cloudnode: &fakeCloudNode{}, leases: leases}
	lease, err := c.AcquireCollectorPublishLease(context.Background(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 7, lease.FencingToken)
	renewed, err := c.RenewCollectorPublishLease(context.Background(), lease)
	require.NoError(t, err)
	require.Equal(t, lease.LeaseID, renewed.LeaseID)
	require.NoError(t, c.ReleaseCollectorPublishLease(context.Background(), renewed))
	require.Equal(t, []string{"acquire:holder-1", "renew", "release"}, leases.calls)
	require.Equal(t, []string{"crypto", "crypto", "crypto"}, leases.spaces)
}

func TestSubmitRuntimeConfigsRejectsMissingJobIdentityAsAmbiguous(t *testing.T) {
	for name, rsp := range map[string]*cloudnodepb.SubmitNodeBatchRsp{
		"empty response": nil,
		"no job id":      {RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS}},
		"blank job id":   {RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS}, JobId: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{cloudnode: &fakeCloudNode{submitRsp: rsp}, leases: &fakeLeases{}}
			jobID, err := c.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
			require.ErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
			require.Empty(t, jobID)
		})
	}
}

func TestSubmitRuntimeConfigsClassifiesTransportOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		unknown bool
	}{
		"超时":      {err: errs.NewFrameError(errs.RetClientTimeout, "timeout"), unknown: true},
		"读响应中断":   {err: errs.NewFrameError(errs.RetClientReadFrameErr, "EOF"), unknown: true},
		"服务端错误":   {err: errs.New(errs.RetServerSystemErr, "panic"), unknown: true},
		"连接失败":    {err: errs.NewFrameError(errs.RetClientConnectFail, "refused"), unknown: false},
		"网关拒绝调用方": {err: errs.New(gatewayroute.RetForbidden, "forbidden"), unknown: false},
		"服务不在主机":  {err: errs.New(gatewayroute.RetServiceNotHere, "not here"), unknown: false},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Client{cloudnode: &fakeCloudNode{submitErr: tc.err}, leases: &fakeLeases{}}
			_, err := c.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
			require.Error(t, err)
			require.Equal(t, tc.unknown, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
		})
	}
}

func TestSubmitRuntimeConfigsKeepsLocalAndExplicitBusinessErrorsDefinite(t *testing.T) {
	t.Run("local configuration", func(t *testing.T) {
		_, err := (&Client{}).SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
	})
	t.Run("business rejection", func(t *testing.T) {
		c := &Client{cloudnode: &fakeCloudNode{submitRsp: &cloudnodepb.SubmitNodeBatchRsp{RetInfo: &cloudnodepb.RetInfo{Code: 14, Msg: "publish lease conflict"}}}, leases: &fakeLeases{}}
		_, err := c.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
	})
}

func TestRenewCollectorPublishLeaseClassifiesConflictAsStale(t *testing.T) {
	leases := &fakeLeases{renewRsp: &adminpb.CollectorPublishLeaseRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "collector publish lease is expired or fenced"}}}
	c := &Client{cloudnode: &fakeCloudNode{}, leases: leases}
	_, err := c.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7})
	require.ErrorIs(t, err, ErrCollectorPublishLeaseStale)
}

func TestRenewCollectorPublishLeaseTransportFailureIsNotStale(t *testing.T) {
	leases := &fakeLeases{renewErr: errs.New(errs.RetServerSystemErr, "database unavailable")}
	c := &Client{cloudnode: &fakeCloudNode{}, leases: leases}
	_, err := c.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrCollectorPublishLeaseStale))
}
