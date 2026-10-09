package scfinvoker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/packages/gatewayclient"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

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

func TestCollectorPublishLeaseCallsUseCollectorGatewayIdentity(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		require.Equal(t, "collector", r.Header.Get("X-Moox-Caller"))
		require.Equal(t, "crypto", r.Header.Get("X-Space-Id"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/service/publishlease/AcquireCollectorPublishLease", "/api/service/publishlease/RenewCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"7","expires_at":"2026-10-03T12:02:00Z"}`))
		case "/api/service/publishlease/ReleaseCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	lease, err := client.AcquireCollectorPublishLease(context.Background(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 7, lease.FencingToken)
	renewed, err := client.RenewCollectorPublishLease(context.Background(), lease)
	require.NoError(t, err)
	require.Equal(t, lease.LeaseID, renewed.LeaseID)
	require.NoError(t, client.ReleaseCollectorPublishLease(context.Background(), renewed))
	require.Equal(t, []string{
		"/api/service/publishlease/AcquireCollectorPublishLease",
		"/api/service/publishlease/RenewCollectorPublishLease",
		"/api/service/publishlease/ReleaseCollectorPublishLease",
	}, calls)
}

func TestParsePublishFencingToken(t *testing.T) {
	for _, raw := range []string{`7`, `"7"`} {
		token, err := parsePublishFencingToken([]byte(raw))
		require.NoError(t, err)
		require.EqualValues(t, 7, token)
	}
	_, err := parsePublishFencingToken([]byte(`0`))
	require.Error(t, err)
}

type gatewayFunc func(context.Context, string, string, any, any) error

func (f gatewayFunc) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestCloudNodeGatewayKeepsSubmissionUnknownAndBusinessRejectionDistinct(t *testing.T) {
	for _, scenario := range []struct {
		name, job string
		code      cloudnodepb.ErrorCode
		transport error
		unknown   bool
	}{
		{name: "accepted", job: "job-1"},
		{name: "missing job", unknown: true},
		{name: "blank job", job: "  ", unknown: true},
		{name: "connection lost", transport: errors.New("response connection lost"), unknown: true},
		{name: "timeout", transport: context.DeadlineExceeded, unknown: true},
		{name: "business rejection", code: cloudnodepb.ErrorCode(14)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			c := New(Config{Gateway: gatewayFunc(func(ctx context.Context, service, method string, req, rsp any) error {
				calls++
				require.Equal(t, "trpc.moox.cloudnode.CloudNodeMgr", service)
				require.Equal(t, "SubmitUpdateNodeRuntimeConfigs", method)
				require.Equal(t, "crypto", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
				require.Equal(t, "timer-1", req.(*cloudnodepb.BatchUpdateNodeRuntimeConfigsReq).GetNodes()[0].GetNodeId())
				response := rsp.(*cloudnodepb.SubmitNodeBatchRsp)
				response.JobId = scenario.job
				response.RetInfo = &cloudnodepb.RetInfo{Code: scenario.code, Msg: "business rejection"}
				return scenario.transport
			})})
			job, err := c.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
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
		_, err := (&Client{}).SubmitRuntimeConfigs(ctx, "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
	}
}

func TestRenewCollectorPublishLeaseClassifiesConflictAsStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":14,"msg":"collector publish lease is expired or fenced"}}`))
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	_, err := client.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{
		SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7,
	})
	require.ErrorIs(t, err, ErrCollectorPublishLeaseStale)
}

func TestRenewCollectorPublishLeaseTransportFailureIsNotStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	_, err := client.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{
		SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7,
	})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrCollectorPublishLeaseStale))
}
