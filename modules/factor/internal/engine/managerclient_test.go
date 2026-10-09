package engine

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

// fakeFactorEngine 是 moox-factor-mgr 的 FactorEngine 服务桩，记录收到的请求和签名元数据。
type fakeFactorEngine struct {
	factorpb.UnimplementedFactorEngine
	caller     string
	targetNode string
	syncReq    *factorpb.SyncEngineCatalogReq
	sync       *factorpb.SyncEngineCatalogRsp
	heartbeat  *factorpb.EngineHeartbeatRsp
	pull       *factorpb.PullRecalcJobRsp
}

func (f *fakeFactorEngine) SyncEngineCatalog(ctx context.Context, req *factorpb.SyncEngineCatalogReq) (*factorpb.SyncEngineCatalogRsp, error) {
	f.caller = string(trpc.GetMetaData(ctx, "X-Moox-Caller"))
	f.targetNode = string(trpc.GetMetaData(ctx, "X-Moox-Target-Node"))
	f.syncReq = req
	return f.sync, nil
}

func (f *fakeFactorEngine) EngineHeartbeat(context.Context, *factorpb.EngineHeartbeatReq) (*factorpb.EngineHeartbeatRsp, error) {
	return f.heartbeat, nil
}

func (f *fakeFactorEngine) PullRecalcJob(context.Context, *factorpb.PullRecalcJobReq) (*factorpb.PullRecalcJobRsp, error) {
	return f.pull, nil
}

// managerClientFor 在本机启动 FactorEngine 桩，返回经外部方式 gatewayclient 访问它的客户端：桩扮演外部接入。
func managerClientFor(t *testing.T, fake *fakeFactorEngine) *ManagerClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.factor.FactorEngine"), server.WithListener(listener))
	factorpb.RegisterFactorEngineService(service, fake)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	credentials := gatewayauth.Credentials{KeyID: "factor-engine-1", Caller: "factor-engine", Secret: "0123456789abcdef0123456789abcdef"}
	gateway, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{
			Mode: gatewayclient.ModeAccess, Caller: "factor-engine", AccessAddress: listener.Addr().String(), AccessID: "access@storage",
		},
		Credentials: &credentials,
	})
	require.NoError(t, err)
	t.Cleanup(gateway.Close)
	return NewManagerClient(gateway, ManagerConfig{Timeout: 5 * time.Second}, domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"})
}

func TestManagerClientCallsFactorEngineThroughAccess(t *testing.T) {
	fake := &fakeFactorEngine{sync: &factorpb.SyncEngineCatalogRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, CatalogHash: "hash-1",
		Sets: []*factorpb.EngineSet{{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}, ResultReady: true,
			Factors: []*factorpb.FactorDef{{FactorId: "bias", SourceCode: "src"}}}},
	}}
	client := managerClientFor(t, fake)

	snapshot, err := client.SyncCatalog(t.Context(), "hash-0")

	require.NoError(t, err)
	require.Equal(t, "factor-engine", fake.caller, "以 factor-engine 身份签名")
	require.Equal(t, "access@storage", fake.targetNode, "签名的目标是外部接入实例")
	require.Equal(t, "hash-0", fake.syncReq.GetKnownHash())
	require.Equal(t, "factor-engine@mac", fake.syncReq.GetEngine().GetEngineId())
	require.Equal(t, "hash-1", snapshot.Hash)
	require.Len(t, snapshot.Sets, 1)
	require.True(t, snapshot.Sets[0].ResultReady)
	require.Equal(t, "src", snapshot.Sets[0].Factors[0].SourceCode)
}

func TestManagerClientHeartbeatAndConflict(t *testing.T) {
	fake := &fakeFactorEngine{heartbeat: &factorpb.EngineHeartbeatRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, LeaseTtlSeconds: 45}}
	client := managerClientFor(t, fake)
	ttl, err := client.Heartbeat(t.Context(), domain.EngineStatus{})
	require.NoError(t, err)
	require.Equal(t, 45*time.Second, ttl)

	fake.heartbeat = &factorpb.EngineHeartbeatRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "held by factor-engine@other"}}
	_, err = client.Heartbeat(t.Context(), domain.EngineStatus{})
	require.ErrorIs(t, err, ErrLeaseConflict)
}

func TestManagerClientRejectsTransportErrors(t *testing.T) {
	fake := &fakeFactorEngine{}
	client := managerClientFor(t, fake)
	// 桩没有实现 ReportRecalcProgress，tRPC 返回传输层错误，不能当成租约冲突。
	_, err := client.ReportRecalcProgress(t.Context(), "job-1", "token", time.Now(), "running", "")
	require.ErrorContains(t, err, "call ReportRecalcProgress")
	require.NotErrorIs(t, err, ErrLeaseConflict)
}

func TestManagerClientPullDecodesWindow(t *testing.T) {
	client := managerClientFor(t, &fakeFactorEngine{pull: &factorpb.PullRecalcJobRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Found: true, LeaseToken: "token",
		Job: &factorpb.RecalcJob{JobId: "job-1", StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T01:00:00Z",
			ProgressTime: "2026-10-04T00:30:00Z", Subjects: []string{"BTC"}},
		Set: &factorpb.EngineSet{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}},
	}})

	job, found, err := client.PullRecalcJob(t.Context())

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "token", job.LeaseToken)
	require.Equal(t, time.Date(2026, 10, 4, 0, 30, 0, 0, time.UTC), job.Window.Progress)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, "fset_a", job.Set.Set.SetID)
}
