package scfinvoker

import (
	"context"
	"errors"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestIsDeployedRequiresPostDeployMarkerWhenDeploymentIDIsEmpty(t *testing.T) {
	if isDeployed(&cloudfunctionpb.FunctionInstance{PackageId: "pkg-1"}) {
		t.Fatal("package-only node must not be invokable before deploy")
	}
	if !isDeployed(&cloudfunctionpb.FunctionInstance{PackageId: "pkg-1", DeploymentId: "dep-1"}) {
		t.Fatal("node with deployment id should be invokable")
	}
	metadata, err := structpb.NewStruct(map[string]any{"deployment_ready": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isDeployed(&cloudfunctionpb.FunctionInstance{PackageId: "pkg-1", Metadata: metadata}) {
		t.Fatal("node with local deployment marker should be invokable")
	}
}

func TestCollectorPublishLeaseCallsBorrowProcessGateway(t *testing.T) {
	var calls []string
	client := New(Config{Gateway: gatewayFunc(func(ctx context.Context, service, method string, req, rsp any) error {
		require.Equal(t, "trpc.moox.admin.CollectorPublishLease", service)
		require.Equal(t, "crypto", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
		calls = append(calls, method)
		switch method {
		case "AcquireCollectorPublishLease":
			require.Equal(t, "holder-1", req.(*adminpb.AcquireCollectorPublishLeaseReq).GetHolderId())
		case "RenewCollectorPublishLease":
			require.EqualValues(t, 7, req.(*adminpb.RenewCollectorPublishLeaseReq).GetFencingToken())
		case "ReleaseCollectorPublishLease":
			require.EqualValues(t, 7, req.(*adminpb.ReleaseCollectorPublishLeaseReq).GetFencingToken())
			*rsp.(*adminpb.ReleaseCollectorPublishLeaseRsp) = adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Released: true}
			return nil
		default:
			t.Fatalf("unexpected method %s", method)
		}
		*rsp.(*adminpb.CollectorPublishLeaseRsp) = adminpb.CollectorPublishLeaseRsp{
			RetInfo: &adminpb.RetInfo{}, SpaceId: "crypto", LeaseId: "lease-1", FencingToken: 7, ExpiresAt: "2026-10-03T12:02:00Z",
		}
		return nil
	})})
	lease, err := client.AcquireCollectorPublishLease(t.Context(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 7, lease.FencingToken)
	renewed, err := client.RenewCollectorPublishLease(t.Context(), lease)
	require.NoError(t, err)
	require.Equal(t, lease.LeaseID, renewed.LeaseID)
	require.NoError(t, client.ReleaseCollectorPublishLease(t.Context(), renewed))
	require.Equal(t, []string{"AcquireCollectorPublishLease", "RenewCollectorPublishLease", "ReleaseCollectorPublishLease"}, calls)
}

func TestAcquireCollectorPublishLeaseSeparatesHeldFromOtherFailures(t *testing.T) {
	respond := func(code adminpb.ErrorCode, msg string, transport error) *Client {
		return New(Config{Gateway: gatewayFunc(func(_ context.Context, _, _ string, _, rsp any) error {
			if transport != nil {
				return transport
			}
			*rsp.(*adminpb.CollectorPublishLeaseRsp) = adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{Code: code, Msg: msg}}
			return nil
		})})
	}
	// Admin 明确答复 CONFLICT：租约或在途操作占用仍被持有，是租约在正常工作。
	_, err := respond(adminpb.ErrorCode_CONFLICT, "collector publish lease is held", nil).AcquireCollectorPublishLease(t.Context(), "crypto", "holder-1")
	require.ErrorIs(t, err, ErrCollectorPublishLeaseHeld)
	require.Contains(t, err.Error(), "collector publish lease is held")
	// 无权限、参数错误、Admin 不可达、响应异常都不是「被占用」，不能享受宽限。
	for name, client := range map[string]*Client{
		"no permission": respond(adminpb.ErrorCode_NO_PERMISSION, "requires space owner or admin", nil),
		"invalid param": respond(adminpb.ErrorCode_INVALID_PARAM, "space_id and holder_id are required", nil),
		"transport":     respond(0, "", errors.New("dial tcp: connection refused")),
		"incomplete":    respond(adminpb.ErrorCode_SUCCESS, "", nil),
	} {
		_, err := client.AcquireCollectorPublishLease(t.Context(), "crypto", "holder-1")
		require.Error(t, err, name)
		require.NotErrorIs(t, err, ErrCollectorPublishLeaseHeld, name)
	}
}

type gatewayFunc func(context.Context, string, string, any, any) error

func (f gatewayFunc) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestCloudFunctionGatewayKeepsSubmissionUnknownAndBusinessRejectionDistinct(t *testing.T) {
	for _, scenario := range []struct {
		name, job string
		code      cloudfunctionpb.ErrorCode
		transport error
		unknown   bool
	}{
		{name: "accepted", job: "job-1"},
		{name: "missing job", unknown: true},
		{name: "blank job", job: "  ", unknown: true},
		{name: "connection lost", transport: errors.New("response connection lost"), unknown: true},
		{name: "timeout", transport: context.DeadlineExceeded, unknown: true},
		{name: "business rejection", code: cloudfunctionpb.ErrorCode(14)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			c := New(Config{Gateway: gatewayFunc(func(ctx context.Context, service, method string, req, rsp any) error {
				calls++
				require.Equal(t, "trpc.moox.cloudfunction.CloudFunctionMgr", service)
				require.Equal(t, "SubmitUpdateFunctionInstanceRuntimeConfigs", method)
				require.Equal(t, "crypto", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
				require.Equal(t, "timer-1", req.(*cloudfunctionpb.SubmitUpdateFunctionInstanceRuntimeConfigsReq).GetInstances()[0].GetInstanceId())
				response := rsp.(*cloudfunctionpb.SubmitFunctionJobRsp)
				response.JobId = scenario.job
				response.RetInfo = &cloudfunctionpb.RetInfo{Code: scenario.code, Msg: "business rejection"}
				return scenario.transport
			})})
			job, err := c.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch{{InstanceId: "timer-1"}})
			require.Equal(t, 1, calls, "writes are never reissued")
			if scenario.name == "accepted" {
				require.NoError(t, err)
				require.Equal(t, "job-1", job)
			} else {
				require.Error(t, err)
				require.Equal(t, scenario.unknown, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
			}
		})
	}
	for _, ctx := range []context.Context{context.Background(), func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }()} {
		_, err := (&Client{}).SubmitRuntimeConfigs(ctx, "crypto", []*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch{{InstanceId: "timer-1"}})
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
	}
}

func TestRenewCollectorPublishLeaseDistinguishesConflictAndTransport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      adminpb.ErrorCode
		transport error
		stale     bool
	}{
		{name: "fenced", code: adminpb.ErrorCode_CONFLICT, stale: true},
		{name: "transport", transport: errors.New("response lost")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := New(Config{Gateway: gatewayFunc(func(_ context.Context, _, _ string, _, rsp any) error {
				calls++
				rsp.(*adminpb.CollectorPublishLeaseRsp).RetInfo = &adminpb.RetInfo{Code: tc.code}
				return tc.transport
			})})
			_, err := client.RenewCollectorPublishLease(t.Context(), &CollectorPublishLease{SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7})
			require.Error(t, err)
			require.Equal(t, tc.stale, errors.Is(err, ErrCollectorPublishLeaseStale))
			require.Equal(t, 1, calls)
		})
	}
}
