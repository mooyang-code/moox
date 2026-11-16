package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// managerDirectory 是固定的服务目录：测试里 factor-mgr 的服务就在引擎所在的主机上，请求经本机网关入口进来。
type managerDirectory struct{ directory servicecatalog.Directory }

func (d managerDirectory) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: version != d.directory.Version, Directory: d.directory}, nil
}

func testCAFile(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return path
}

func managerClientFor(t *testing.T, handler func(context.Context, string, []byte) (proto.Message, error)) *ManagerClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// 引擎是普通调用方：用所在主机（factor）的密钥、以自己的组件 ID 签名。
	credentials := authtest.Component("factor-engine", "factor")
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
			_, err := authtest.Verify(credentials, gatewayauth.Request{TargetNode: "factor", Callee: managerServicePath, Func: method, Body: request.Data}, headers, time.Now())
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
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"factor": {Address: "127.0.0.1"}}, Services: map[string][]string{managerServicePath: {"factor"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: credentials, LocalHostID: "factor", LocalAddress: listener.Addr().String(),
		CAFile: testCAFile(t), Source: managerDirectory{directory}, Serialization: codec.SerializationTypePB})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client, err := NewManagerClient(gateway, 5*time.Second, domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"})
	require.NoError(t, err)
	return client
}

func TestManagerClientSignsNativeRequestsAsHostComponent(t *testing.T) {
	client := managerClientFor(t, func(_ context.Context, method string, body []byte) (proto.Message, error) {
		require.Equal(t, "SyncEngineCatalog", method)
		var req factorpb.SyncEngineCatalogReq
		require.NoError(t, proto.Unmarshal(body, &req))
		require.Equal(t, "hash-0", req.GetKnownHash())
		require.Equal(t, "factor-engine@mac", req.GetEngine().GetEngineId())
		return &factorpb.SyncEngineCatalogRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, CatalogHash: "hash-1", Tasks: []*factorpb.EngineTask{{FactorTask: &factorpb.FactorTask{TaskId: "ftask_a"}, ResultReady: true, Members: []*factorpb.EngineMember{{MemberKey: "bias", Factor: &factorpb.FactorDef{FactorId: "bias", Name: "bias", Spec: "output: close\n"}}}}}}, nil
	})
	snapshot, err := client.SyncCatalog(t.Context(), "hash-0")
	require.NoError(t, err)
	require.Equal(t, "hash-1", snapshot.Hash)
	require.Len(t, snapshot.Sets, 1)
	require.True(t, snapshot.Sets[0].ResultReady)
	require.Equal(t, "output: close\n", snapshot.Sets[0].Members[0].Factor.Spec)
	require.Equal(t, []string{"bias"}, snapshot.Sets[0].Members[0].Factor.Outputs, "接收方编译得到派生字段")
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
		return &factorpb.PullRecalcJobRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Found: true, LeaseToken: "token", Job: &factorpb.RecalcJob{JobId: "job-1", StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T01:00:00Z", ProgressTime: "2026-10-04T00:30:00Z", Subjects: []string{"BTC"}}, Task: &factorpb.EngineTask{FactorTask: &factorpb.FactorTask{TaskId: "ftask_a"}}}, nil
	})
	job, found, err := client.PullRecalcJob(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "token", job.LeaseToken)
	require.Equal(t, time.Date(2026, 10, 4, 0, 30, 0, 0, time.UTC), job.Window.Progress)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, "ftask_a", job.Set.Set.TaskID)
	status, err := client.ReportRecalcProgress(t.Context(), job.JobID, job.LeaseToken, job.Window.Progress, "running", "", nil)
	require.NoError(t, err)
	require.Equal(t, "running", status)
}
