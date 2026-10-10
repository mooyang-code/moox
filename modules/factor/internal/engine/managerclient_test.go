package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const testEngineSecret = "0123456789abcdef0123456789abcdef"

func managerClientFor(t *testing.T, handler func(context.Context, string, []byte) (proto.Message, error)) *ManagerClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	credentials := gatewayauth.Credentials{Caller: "factor-engine", KeyID: "assigned-engine-key-38", Secret: testEngineSecret}
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(managerServicePath))
	require.NoError(t, service.Register(&server.ServiceDesc{ServiceName: managerServicePath, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
		request := &codec.Body{}
		filters, err := decode(request)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, request, func(ctx context.Context, input interface{}) (interface{}, error) {
			message := codec.Message(ctx)
			method := strings.TrimPrefix(message.ServerRPCName(), "/"+managerServicePath+"/")
			headers := http.Header{}
			for key, value := range message.ServerMetaData() {
				headers.Add(key, string(value))
			}
			_, err := gatewayauth.Verify(credentials, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: "access@storage", Callee: managerServicePath, Func: method, Body: request.Data}, headers, time.Now())
			if err != nil {
				return nil, err
			}
			response, err := handler(ctx, method, request.Data)
			if err != nil {
				return nil, err
			}
			body, err := proto.Marshal(response)
			return &codec.Body{Data: body}, err
		})
	}}}}, struct{}{}))
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.External, Credentials: credentials, AccessAddress: listener.Addr().String(), AccessInstanceID: "access@storage"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client, err := NewManagerClient(gateway, 5*time.Second, domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"})
	require.NoError(t, err)
	return client
}

func TestManagerClientSignsNativeRequestsWithAssignedExternalIdentity(t *testing.T) {
	client := managerClientFor(t, func(_ context.Context, method string, body []byte) (proto.Message, error) {
		require.Equal(t, "SyncEngineCatalog", method)
		var req factorpb.SyncEngineCatalogReq
		require.NoError(t, proto.Unmarshal(body, &req))
		require.Equal(t, "hash-0", req.GetKnownHash())
		require.Equal(t, "factor-engine@mac", req.GetEngine().GetEngineId())
		return &factorpb.SyncEngineCatalogRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, CatalogHash: "hash-1", Sets: []*factorpb.EngineSet{{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}, ResultReady: true, Factors: []*factorpb.FactorDef{{FactorId: "bias", SourceCode: "src"}}}}}, nil
	})
	snapshot, err := client.SyncCatalog(t.Context(), "hash-0")
	require.NoError(t, err)
	require.Equal(t, "hash-1", snapshot.Hash)
	require.Len(t, snapshot.Sets, 1)
	require.True(t, snapshot.Sets[0].ResultReady)
	require.Equal(t, "src", snapshot.Sets[0].Factors[0].SourceCode)
}

func TestManagerClientUsesNativeTransportAndMapsLeaseConflict(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	for _, conflict := range []bool{false, true} {
		client := managerClientFor(t, func(_ context.Context, method string, _ []byte) (proto.Message, error) {
			require.Equal(t, "EngineHeartbeat", method)
			ret := &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
			if conflict {
				ret.Code = commonpb.ErrorCode_CONFLICT
				ret.Msg = "lease held by another engine"
			}
			return &factorpb.EngineHeartbeatRsp{RetInfo: ret, LeaseTtlSeconds: 45}, nil
		})
		ttl, err := client.Heartbeat(t.Context(), domain.EngineStatus{})
		if conflict {
			require.ErrorIs(t, err, ErrLeaseConflict)
		} else {
			require.NoError(t, err)
			require.Equal(t, 45*time.Second, ttl)
		}
	}
}

func TestManagerClientPreservesNativeFailureAndRejectsMissingBusinessStatus(t *testing.T) {
	client := managerClientFor(t, func(context.Context, string, []byte) (proto.Message, error) {
		return nil, errs.NewFrameError(errs.RetServerSystemErr, "native failure")
	})
	_, err := client.Heartbeat(t.Context(), domain.EngineStatus{})
	require.ErrorContains(t, err, "native failure")
	require.NotErrorIs(t, err, ErrLeaseConflict)
	client = managerClientFor(t, func(context.Context, string, []byte) (proto.Message, error) {
		return &factorpb.EngineHeartbeatRsp{}, nil
	})
	_, err = client.Heartbeat(t.Context(), domain.EngineStatus{})
	require.ErrorContains(t, err, "no ret_info")
	_, err = NewManagerClient(nil, time.Second, domain.EngineIdentity{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrLeaseConflict))
}

func TestManagerClientPullDecodesWindowAndProgressRetainsFence(t *testing.T) {
	client := managerClientFor(t, func(_ context.Context, method string, body []byte) (proto.Message, error) {
		if method == "ReportRecalcProgress" {
			var request factorpb.ReportRecalcProgressReq
			require.NoError(t, proto.Unmarshal(body, &request))
			require.Equal(t, "token", request.GetLeaseToken())
			require.Equal(t, "job-1", request.GetJobId())
			return &factorpb.ReportRecalcProgressRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, JobStatus: "running"}, nil
		}
		require.Equal(t, "PullRecalcJob", method)
		return &factorpb.PullRecalcJobRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Found: true, LeaseToken: "token", Job: &factorpb.RecalcJob{JobId: "job-1", StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T01:00:00Z", ProgressTime: "2026-10-04T00:30:00Z", Subjects: []string{"BTC"}}, Set: &factorpb.EngineSet{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}}}, nil
	})
	job, found, err := client.PullRecalcJob(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "token", job.LeaseToken)
	require.Equal(t, time.Date(2026, 10, 4, 0, 30, 0, 0, time.UTC), job.Window.Progress)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, "fset_a", job.Set.Set.SetID)
	status, err := client.ReportRecalcProgress(t.Context(), job.JobID, job.LeaseToken, job.Window.Progress, "running", "")
	require.NoError(t, err)
	require.Equal(t, "running", status)
}
