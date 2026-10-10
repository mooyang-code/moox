package accessproxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

const scfFlowHostID = "storage"

// TestSCFFlowThroughAccessAndHostGateway 在本地模拟一次 SCF 的完整流程：以外部方式经外部接入领取 Timer 批次、写入
// 时序批次、上报失败。外部接入以 access 身份经真实的主机网关本机入口（e2e-helper）转发，上游收到的 PB 与 SCF 发出的
// 完全一致，并能从元数据看到外部调用方；白名单之外的方法在外部接入处被拒绝，不会到达上游。
func TestSCFFlowThroughAccessAndHostGateway(t *testing.T) {
	upstream := &scfFlowUpstream{}
	storageAddress := serveSCFFlowUpstream(t, "trpc.moox.storage.PrimaryStore", func(s server.Service) {
		storagepb.RegisterPrimaryStoreService(s, &scfFlowPrimaryStore{upstream: upstream})
	})
	runtimeAddress := serveSCFFlowUpstream(t, "trpc.moox.collector.MarketFetchRuntime", func(s server.Service) {
		collectorpb.RegisterMarketFetchRuntimeService(s, &scfFlowRuntime{upstream: upstream})
	})
	accessKey := gatewayauth.CallerKey{Caller: servicecatalog.AccessCaller, KeyID: "access-1", Secret: "access-e2e-secret"}
	hostGateway := startSCFFlowHostGateway(t, accessKey, storageAddress, runtimeAddress)
	accessAddress := startSCFFlowAccess(t, hostGateway, accessKey)

	scf := newSCFFlowClient(t, accessAddress, scfCollector)
	runtime := collectorpb.NewMarketFetchRuntimeClientProxy(scf.ClientOptions()...)
	storage := storagepb.NewPrimaryStoreClientProxy(scf.ClientOptions()...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	claimReq := &collectorpb.ClaimTimerBatchReq{SpaceId: "stockcn", FunctionName: "moox-fetcher-stockcn-0", RequestId: "scf-request-1", GroupCount: 1, BindingHash: "binding", TickTime: 1_800_000_000}
	claim, err := runtime.ClaimTimerBatch(ctx, claimReq)
	require.NoError(t, err)
	require.True(t, claim.GetClaimed())

	commitReq := &storagepb.PrimaryCommitTimeSeriesBatchReq{SourceEventId: "scf-batch-1", Expectation: &storagepb.DatasetPeriodExpectation{SpaceId: "stockcn", DatasetId: "dataset", Frequency: "1m", PeriodTime: 1_790_000_000}}
	_, err = storage.CommitTimeSeriesBatch(ctx, commitReq)
	require.NoError(t, err)

	failuresReq := &storagepb.PrimaryRecordDatasetPeriodFailuresReq{SeriesIndexes: []uint32{1, 2}, Expectation: commitReq.GetExpectation()}
	_, err = storage.RecordDatasetPeriodFailures(ctx, failuresReq)
	require.NoError(t, err)

	calls := upstream.snapshot()
	require.Len(t, calls, 3)
	for index, want := range []proto.Message{claimReq, commitReq, failuresReq} {
		require.True(t, proto.Equal(want, calls[index].request), "上游收到的 %s 与 SCF 发出的不一致", calls[index].method)
		require.Equal(t, scfCollector.Caller, calls[index].principal, "上游从元数据看到的外部调用方")
	}

	// scf-collector 的白名单不含写因子；factor-engine 不能提交行情批次。两者都在外部接入处被拒绝。
	_, err = storage.WriteFactorRows(ctx, &storagepb.PrimaryWriteFactorRowsReq{})
	require.EqualValues(t, gatewayroute.RetForbidden, errs.Code(err), "错误: %v", err)
	factor := storagepb.NewPrimaryStoreClientProxy(newSCFFlowClient(t, accessAddress, factorEngine).ClientOptions()...)
	_, err = factor.CommitTimeSeriesBatch(ctx, commitReq)
	require.EqualValues(t, gatewayroute.RetForbidden, errs.Code(err), "错误: %v", err)
	require.Len(t, upstream.snapshot(), 3, "被拒绝的请求不能到达上游")
}

type scfFlowCall struct {
	method    string
	principal string
	request   proto.Message
}

type scfFlowUpstream struct {
	mu    sync.Mutex
	calls []scfFlowCall
}

func (u *scfFlowUpstream) record(ctx context.Context, method string, request proto.Message) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, scfFlowCall{method: method, principal: string(trpc.GetMetaData(ctx, gatewayroute.MetadataAccessPrincipal)), request: proto.Clone(request)})
}

func (u *scfFlowUpstream) snapshot() []scfFlowCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]scfFlowCall(nil), u.calls...)
}

type scfFlowPrimaryStore struct {
	storagepb.UnimplementedPrimaryStore
	upstream *scfFlowUpstream
}

func (s *scfFlowPrimaryStore) CommitTimeSeriesBatch(ctx context.Context, req *storagepb.PrimaryCommitTimeSeriesBatchReq) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	s.upstream.record(ctx, "CommitTimeSeriesBatch", req)
	return &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (s *scfFlowPrimaryStore) RecordDatasetPeriodFailures(ctx context.Context, req *storagepb.PrimaryRecordDatasetPeriodFailuresReq) (*storagepb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	s.upstream.record(ctx, "RecordDatasetPeriodFailures", req)
	return &storagepb.PrimaryRecordDatasetPeriodFailuresRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (s *scfFlowPrimaryStore) WriteFactorRows(ctx context.Context, req *storagepb.PrimaryWriteFactorRowsReq) (*storagepb.PrimaryWriteFactorRowsRsp, error) {
	s.upstream.record(ctx, "WriteFactorRows", req)
	return &storagepb.PrimaryWriteFactorRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

type scfFlowRuntime struct {
	collectorpb.UnimplementedMarketFetchRuntime
	upstream *scfFlowUpstream
}

func (r *scfFlowRuntime) ClaimTimerBatch(ctx context.Context, req *collectorpb.ClaimTimerBatchReq) (*collectorpb.ClaimTimerBatchRsp, error) {
	r.upstream.record(ctx, "ClaimTimerBatch", req)
	return &collectorpb.ClaimTimerBatchRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}, Claimed: true, RequestJson: []byte(`{}`)}, nil
}

// serveSCFFlowUpstream 在本机随机端口上启动一个上游 tRPC 服务。
func serveSCFFlowUpstream(t *testing.T, serviceName string, register func(server.Service)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName(serviceName), server.WithListener(listener))
	register(service)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	return listener.Addr().String()
}

// startSCFFlowHostGateway 启动真实的主机网关本机入口（e2e-helper），向 access 开放组件目录允许它调用的方法。
func startSCFFlowHostGateway(t *testing.T, accessKey gatewayauth.CallerKey, storageAddress, runtimeAddress string) string {
	t.Helper()
	dir := t.TempDir()
	helper := filepath.Join(dir, "e2e-helper")
	build := exec.Command("go", "build", "-o", helper, "./cmd/e2e-helper")
	build.Dir = filepath.Join("..", "..", "..", "hostgateway")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "编译 e2e-helper: %s", output)

	readyFile := filepath.Join(dir, "ready")
	command := exec.Command(helper,
		"-host-id", scfFlowHostID,
		"-route", "trpc.moox.storage.PrimaryStore="+storageAddress,
		"-route", "trpc.moox.collector.MarketFetchRuntime="+runtimeAddress,
		"-callers", servicecatalog.AccessCaller,
		"-ready-file", readyFile, "-nonce-dir", filepath.Join(dir, "nonces"),
	)
	command.Env = append(os.Environ(), "MOOX_GATEWAY_E2E_KEYS="+fmt.Sprintf("%s:%s:%s", accessKey.Caller, accessKey.KeyID, accessKey.Secret))
	logs := &lockedBuffer{}
	command.Stdout, command.Stderr = logs, logs
	require.NoError(t, command.Start())
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-exited
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if raw, err := os.ReadFile(readyFile); err == nil && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimPrefix(strings.TrimSpace(string(raw)), "ip://")
		}
		select {
		case err := <-exited:
			t.Fatalf("e2e-helper 提前退出: %v\n%s", err, logs.String())
		default:
		}
		require.True(t, time.Now().Before(deadline), "e2e-helper 没有就绪: %s", logs.String())
		time.Sleep(20 * time.Millisecond)
	}
}

// startSCFFlowAccess 在本机随机端口上启动外部接入：以 access 身份经 hostGateway 转发，登记 scf-collector 和
// factor-engine 两个外部调用方。
func startSCFFlowAccess(t *testing.T, hostGateway string, accessKey gatewayauth.CallerKey) string {
	t.Helper()
	accessCredentials, err := accessKey.Credentials()
	require.NoError(t, err)
	upstream, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{
			Mode: gatewayclient.ModeLocal, Caller: servicecatalog.AccessCaller, CAFile: "unused-moox-ca.crt",
			CacheDir: t.TempDir(), LocalAddress: hostGateway,
		},
		Credentials: &accessCredentials,
	})
	require.NoError(t, err)
	t.Cleanup(upstream.Close)
	registry, err := gatewayauth.NewCredentialRegistry([]gatewayauth.Credentials{scfCollector, factorEngine})
	require.NoError(t, err)
	nonces, err := OpenSQLiteNonces(filepath.Join(t.TempDir(), "nonces.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nonces.Close() })
	proxy, err := New(Options{Registry: registry, Catalog: servicecatalog.Default(), Upstream: upstream, Nonces: nonces, Metrics: &fakeMetrics{}, Timeout: 10 * time.Second})
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(
		server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName(AccessServiceName),
		server.WithListener(listener), server.WithCurrentSerializationType(codec.SerializationTypeNoop),
	)
	require.NoError(t, RegisterAccessService(service, proxy))
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	return listener.Addr().String()
}

// newSCFFlowClient 创建外部方式的 gatewayclient，与 SCF 和因子引擎的用法一致。
func newSCFFlowClient(t *testing.T, accessAddress string, credentials gatewayauth.Credentials) *gatewayclient.Client {
	t.Helper()
	client, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{
			Mode: gatewayclient.ModeAccess, Caller: credentials.Caller, AccessAddress: accessAddress, AccessID: "access@" + scfFlowHostID,
		},
		Credentials: &credentials,
	})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client
}
