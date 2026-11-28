package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	directorypb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// strategyGatewayWire 同时充当本机网关的目录服务与同机的 Storage、Factor、Trade：策略直连它们的服务端口，
// 这里核对请求不带签名头、调用的方法都在服务目录的名单里（跨主机时由网关按名单把关）、请求空间没有变。
type strategyGatewayWire struct {
	factorpb.UnimplementedFactorMgr
	factorpb.UnimplementedEngineControl
	tradepb.UnimplementedTradeConsoleService
	storagepb.UnimplementedMetadata
	storagepb.UnimplementedDataView
	directorypb.UnimplementedDirectory
	catalog   servicecatalog.Catalog
	mu        sync.Mutex
	calls     map[string]int
	refreshes int
	// closedThrough 是 Trade 替身记下的“关闭到”时刻。
	closedThrough int64
	// deadlines 记下 EngineControl 各方法收到的剩余超时：回放“因子现算”不能被 factor.timeout（5 秒）掐断。
	deadlines map[string]time.Duration
}

// 这个替身同时充当 FactorMgr、EngineControl 与 TradeConsoleService，三个服务都有生命周期方法：在这里显式实现一次消除歧义，本测试不调用它们。
func (w *strategyGatewayWire) GetLifecycle(context.Context, *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return nil, errors.New("not used")
}

func (w *strategyGatewayWire) ControlLifecycle(context.Context, *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return nil, errors.New("not used")
}

func (w *strategyGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == strategyDirectoryVersion {
		return &directorypb.GetDirectoryRsp{Version: strategyDirectoryVersion}, nil
	}
	// 目录只有主机与部署：策略与 Trade、Factor、Storage 同机。
	rsp := &directorypb.GetDirectoryRsp{Changed: true, Version: strategyDirectoryVersion,
		Hosts: []*directorypb.DirectoryHost{{HostId: "control", Address: "control.example.test", Reachability: "direct", Enabled: true}}}
	for _, component := range []string{"strategy", "trade", "factor-mgr", "factor-engine", "storage-primary", "storage-view"} {
		rsp.Deployments = append(rsp.Deployments, &directorypb.DirectoryDeployment{HostId: "control", ComponentId: component, Enabled: true})
	}
	return rsp, nil
}

const strategyDirectoryVersion = "v1"

func (w *strategyGatewayWire) verify(ctx context.Context, service, method string, _ proto.Message) error {
	if !w.catalog.Allowed("strategy", service, method) {
		return fmt.Errorf("strategy 调用 %s/%s 不在服务目录的 ACL 里", service, method)
	}
	for name := range codec.Message(ctx).ServerMetaData() {
		if strings.HasPrefix(strings.ToLower(name), "x-moox-") {
			return fmt.Errorf("同机直连的请求带了签名头 %s", name)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls[service+"/"+method]++
	return nil
}

type strategyStorageRequest interface {
	proto.Message
	GetSpaceId() string
}

func (w *strategyGatewayWire) verifyStorage(ctx context.Context, service, method string, req strategyStorageRequest) error {
	if err := w.verify(ctx, "trpc.moox.storage."+service, method, req); err != nil {
		return err
	}
	if req.GetSpaceId() != "crypto" {
		return errors.New("strategy 的 Storage 请求空间发生了变化")
	}
	return nil
}

func (w *strategyGatewayWire) GetFactor(ctx context.Context, req *factorpb.GetFactorReq) (*factorpb.GetFactorRsp, error) {
	if err := w.verify(ctx, "trpc.moox.factor.FactorMgr", "GetFactor", req); err != nil {
		return nil, err
	}
	return &factorpb.GetFactorRsp{RetInfo: &commonpb.RetInfo{}, Factor: &factorpb.FactorDef{FactorId: req.GetFactorId(), DefinitionHash: "sha256:fixture"}}, nil
}

// engineCall 校验 EngineControl 的调用并记下剩余超时。
func (w *strategyGatewayWire) engineCall(ctx context.Context, method string, req proto.Message) error {
	if err := w.verify(ctx, "trpc.moox.factor.EngineControl", method, req); err != nil {
		return err
	}
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.deadlines == nil {
		w.deadlines = map[string]time.Duration{}
	}
	w.deadlines[method] = remaining
	return nil
}

func (w *strategyGatewayWire) OpenReplay(ctx context.Context, req *factorpb.OpenReplayReq) (*factorpb.OpenReplayRsp, error) {
	if err := w.engineCall(ctx, "OpenReplay", req); err != nil {
		return nil, err
	}
	return &factorpb.OpenReplayRsp{RetInfo: &commonpb.RetInfo{}, ReplayId: req.GetReplayId(), PeriodTimes: []int64{3600}}, nil
}

func (w *strategyGatewayWire) StepReplay(ctx context.Context, req *factorpb.StepReplayReq) (*factorpb.StepReplayRsp, error) {
	if err := w.engineCall(ctx, "StepReplay", req); err != nil {
		return nil, err
	}
	return &factorpb.StepReplayRsp{RetInfo: &commonpb.RetInfo{}, Periods: []*factorpb.ReplayPeriod{{PeriodTime: req.GetPeriodTime()}}}, nil
}

func (w *strategyGatewayWire) CloseReplay(ctx context.Context, req *factorpb.CloseReplayReq) (*factorpb.CloseReplayRsp, error) {
	if err := w.engineCall(ctx, "CloseReplay", req); err != nil {
		return nil, err
	}
	return &factorpb.CloseReplayRsp{RetInfo: &commonpb.RetInfo{}, Closed: true}, nil
}

func (w *strategyGatewayWire) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListDatasetColumns", req); err != nil {
		return nil, err
	}
	return &storagepb.ListDatasetColumnsRsp{RetInfo: &commonpb.RetInfo{}, Columns: []*storagepb.DatasetColumn{{ColumnName: "close", Status: "active"}}}, nil
}

func (w *strategyGatewayWire) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "GetDataset", req); err != nil {
		return nil, err
	}
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{}, Dataset: &storagepb.Dataset{DatasetId: req.GetDatasetId(), Status: "active", Freq: "1m"}}, nil
}

func (w *strategyGatewayWire) GetTag(ctx context.Context, req *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "GetTag", req); err != nil {
		return nil, err
	}
	return &storagepb.GetTagRsp{RetInfo: &commonpb.RetInfo{}, Tag: &storagepb.Tag{TagId: req.GetTagId(), Attributes: map[string]string{"market_type": "spot"}}}, nil
}

func (w *strategyGatewayWire) ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListDatasetSubjects", req); err != nil {
		return nil, err
	}
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{}, DatasetSubjects: []*storagepb.DatasetSubject{{SpaceId: "crypto", DatasetId: "bars", SubjectId: "BTC", Status: "active"}}}, nil
}

func (w *strategyGatewayWire) ListSubjects(ctx context.Context, req *storagepb.ListSubjectsReq) (*storagepb.ListSubjectsRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListSubjects", req); err != nil {
		return nil, err
	}
	return &storagepb.ListSubjectsRsp{RetInfo: &commonpb.RetInfo{}, Subjects: []*storagepb.Subject{{SubjectId: "BTC", Status: "active"}}}, nil
}

func (w *strategyGatewayWire) ListTagMembers(ctx context.Context, req *storagepb.ListTagMembersReq) (*storagepb.ListTagMembersRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListTagMembers", req); err != nil {
		return nil, err
	}
	return &storagepb.ListTagMembersRsp{RetInfo: &commonpb.RetInfo{}}, nil
}

func (w *strategyGatewayWire) GetViewState(ctx context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	if err := w.verifyStorage(ctx, "DataView", "GetViewState", req); err != nil {
		return nil, err
	}
	return &storagepb.GetViewStateRsp{RetInfo: &commonpb.RetInfo{}, IndexId: "index-1", Generation: 3, AppliedCursor: &storagepb.DatasetChangeCursor{StoreId: "store-1", Sequence: 9}}, nil
}

func (w *strategyGatewayWire) QueryTimeSeriesRows(ctx context.Context, req *storagepb.QueryTimeSeriesRowsReq) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	if err := w.verifyStorage(ctx, "DataView", "QueryTimeSeriesRows", req); err != nil {
		return nil, err
	}
	if req.GetPin().GetIndexId() != "index-1" || len(req.GetSelectors()) != 1 || req.GetSelectors()[0].GetSubjectId() != "BTC" {
		return nil, errors.New("Storage 的 View 索引或选择器发生了变化")
	}
	return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, Complete: true, Index: &storagepb.IndexVersion{IndexId: "index-1", Revision: 7},
		Rows: []*storagepb.Row{{Key: &storagepb.RowKey{Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC", DataTime: req.GetTimeRange().GetStartTime()}}}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "123.45"}}}}}}}, nil
}

// verifyTrade 校验 Trade 调用：不带签名头、在名单里，并带着调用方给出的空间。
func (w *strategyGatewayWire) verifyTrade(ctx context.Context, method string, req proto.Message) error {
	if err := w.verify(ctx, "trpc.moox.trade.TradeConsoleService", method, req); err != nil {
		return err
	}
	if space := string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]); space != "crypto" {
		return fmt.Errorf("Trade 调用缺少可信空间：%q", space)
	}
	return nil
}

func (w *strategyGatewayWire) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq) (*tradepb.GetLogicalAccountRsp, error) {
	if err := w.verifyTrade(ctx, "GetLogicalAccount", req); err != nil {
		return nil, err
	}
	account := &tradepb.LogicalAccount{LogicalAccountId: req.GetLogicalAccountId(), SpaceId: "crypto", Markets: []string{"swap"}, ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY}
	return &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account}, nil
}

func (w *strategyGatewayWire) CloseTargetSession(ctx context.Context, req *tradepb.CloseTargetSessionReq) (*tradepb.CloseTargetSessionRsp, error) {
	if err := w.verifyTrade(ctx, "CloseTargetSession", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.closedThrough = max(w.closedThrough, req.GetSessionStartedAt())
	closed := w.closedThrough
	w.mu.Unlock()
	return &tradepb.CloseTargetSessionRsp{RetInfo: &tradepb.RetInfo{}, ClosedThrough: closed}, nil
}

func strategyGatewayFixture(t *testing.T, natsURL string) (Config, *strategyGatewayWire) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "strategy", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "host.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := authtest.Component("strategy", "control")
	require.NoError(t, os.WriteFile(keyPath, []byte(gatewayauth.EncodeSigningKey(credentials.Key)+"\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// 策略与这些服务同机：直连服务目录里各服务的回环端口，这里都指到同一个假服务上。
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	servicePorts := map[int]bool{}
	for _, path := range []string{"trpc.moox.trade.TradeConsoleService", "trpc.moox.factor.FactorMgr", "trpc.moox.factor.EngineControl", "trpc.moox.storage.Metadata", "trpc.moox.storage.DataView"} {
		spec, ok := catalog.Service(path)
		require.True(t, ok, path)
		servicePorts[spec.Port] = true
	}
	t.Cleanup(gatewayclient.SetLocalServiceAddressForTest(func(port int) string {
		if !servicePorts[port] {
			return "127.0.0.1:1"
		}
		return listener.Addr().String()
	}))
	wire := &strategyGatewayWire{catalog: catalog, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	factorpb.RegisterFactorMgrService(svc, wire)
	factorpb.RegisterEngineControlService(svc, wire)
	tradepb.RegisterTradeConsoleServiceService(svc, wire)
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterDataViewService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	raw := fmt.Sprintf("database: %q\ngateway_client:\n  caller: strategy\n  key_file: ../../secrets/host.key\n  host_id: control\n  local_address: "+listener.Addr().String()+"\n  ca_file: "+caPath+"\n", filepath.Join(root, "data", "strategy", "strategy.db"))
	if natsURL != "" {
		raw += fmt.Sprintf("eventbus:\n  urls: [%q]\n", natsURL)
	}
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := Load(configPath)
	require.NoError(t, err)
	return cfg, wire
}

// Storage、Factor 与 Trade 的全部调用都经共享的网关客户端：同机时直连服务端口、不带签名头；调用的方法都在服务目录的名单里，空间照常带到。
func TestStrategyDependenciesUseSharedDeploymentGateway(t *testing.T) {
	for _, name := range []string{"MOOX_SERVICE_GATEWAY_TARGET", "MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET"} {
		t.Setenv(name, "ip://192.0.2.99:11003")
	}
	cfg, wire := strategyGatewayFixture(t, "")
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gateway.Close() })
	reader := newInputClient(cfg, gateway)
	ctx := t.Context()
	view, err := reader.GetView(ctx, "crypto", "bars")
	require.NoError(t, err)
	require.Equal(t, "bars", view.DatasetID)
	require.Equal(t, "index-1", view.ActiveIndexID)
	require.Equal(t, "index-1@store-1#3", view.Generation)
	require.Len(t, view.Columns, 1)
	dataset, err := reader.GetDataset(ctx, "crypto", "bars")
	require.NoError(t, err)
	require.Equal(t, "1m", dataset.Frequency)
	tag, err := reader.GetTag(ctx, "crypto", "spot-tag")
	require.NoError(t, err)
	require.Equal(t, "spot", tag.MarketType)
	subjects, err := reader.ListDatasetSubjects(ctx, "crypto", "bars")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	require.Equal(t, "BTC", subjects[0].SubjectID)
	_, err = reader.ListTagMembers(ctx, "crypto", "spot-tag")
	require.NoError(t, err)
	rows, revision, err := reader.QueryRows(ctx, "crypto", input.Query{DatasetID: "source", Frequency: "1m", Subjects: subjects, Start: time.Unix(60, 0), End: time.Unix(61, 0), Columns: []string{"close"}, ExpectedIndexID: "index-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(7), revision)
	require.Equal(t, 123.45, rows[0].Values["close"])
	factor, err := reader.GetFactor(ctx, "momentum")
	require.NoError(t, err)
	require.Equal(t, "sha256:fixture", factor.DefinitionHash)

	// 回放“因子现算”经同一个网关客户端调因子引擎：超时按服务目录（OpenReplay 10 分钟、其余 2 分钟），不套 factor.timeout。
	engineClient := input.NewEngineClient(gateway)
	_, err = engineClient.OpenReplay(ctx, &factorpb.OpenReplayReq{ReplayId: "r1", TaskId: "ftask", From: "1970-01-01T01:00:00Z", To: "1970-01-01T02:00:00Z", Columns: []string{"close"}})
	require.NoError(t, err)
	_, err = engineClient.StepReplay(ctx, &factorpb.StepReplayReq{ReplayId: "r1", PeriodTime: 3600, Periods: 1})
	require.NoError(t, err)
	_, err = engineClient.CloseReplay(ctx, &factorpb.CloseReplayReq{ReplayId: "r1"})
	require.NoError(t, err)
	wire.mu.Lock()
	deadlines := maps.Clone(wire.deadlines)
	wire.mu.Unlock()
	require.Greater(t, deadlines["OpenReplay"], 9*time.Minute, "OpenReplay 用目录的 10 分钟超时")
	require.Greater(t, deadlines["StepReplay"], 100*time.Second, "StepReplay 用目录的 2 分钟超时")
	require.Less(t, deadlines["StepReplay"], 3*time.Minute)
	require.Greater(t, deadlines["CloseReplay"], 100*time.Second)

	trade := tradeowner.New(cfg.Trade, gateway)
	require.NoError(t, trade.CheckAccount(ctx, "crypto", "acct-1", []string{"swap"}))
	require.NoError(t, trade.CloseSession(ctx, "crypto", "acct-1", "instance-1", "session-1", 1_700_000_000_000))
	wire.mu.Lock()
	require.Equal(t, int64(1_700_000_000_000), wire.closedThrough)
	wire.mu.Unlock()

	// 网关调用的参数由 ctx 与服务目录决定，不接受 tRPC 客户端选项。
	_, err = reader.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{}, client.WithTarget("ip://192.0.2.99:20100"))
	require.ErrorContains(t, err, "不接受 tRPC 客户端选项")
	_, err = reader.DataView.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{}, client.WithTimeout(time.Second))
	require.ErrorContains(t, err, "不接受 tRPC 客户端选项")

	wire.mu.Lock()
	calls, refreshes := maps.Clone(wire.calls), wire.refreshes
	wire.mu.Unlock()
	for _, want := range []string{
		"trpc.moox.storage.Metadata/ListDatasetColumns", "trpc.moox.storage.Metadata/GetDataset",
		"trpc.moox.storage.Metadata/GetTag", "trpc.moox.storage.Metadata/ListDatasetSubjects", "trpc.moox.storage.Metadata/ListSubjects",
		"trpc.moox.storage.Metadata/ListTagMembers", "trpc.moox.storage.DataView/QueryTimeSeriesRows", "trpc.moox.storage.DataView/GetViewState", "trpc.moox.factor.FactorMgr/GetFactor",
		"trpc.moox.factor.EngineControl/OpenReplay", "trpc.moox.factor.EngineControl/StepReplay", "trpc.moox.factor.EngineControl/CloseReplay",
		"trpc.moox.trade.TradeConsoleService/GetLogicalAccount", "trpc.moox.trade.TradeConsoleService/CloseTargetSession",
	} {
		require.Positive(t, calls[want], want)
	}
	require.GreaterOrEqual(t, refreshes, 1)
	require.NoError(t, gateway.Close())
	_, err = reader.GetFactor(ctx, "momentum")
	require.Error(t, err)
	// 没有网关客户端时调用返回配置错误而不是 panic。
	_, err = input.NewGatewayClient(nil, 0, 0).GetFactor(ctx, "momentum")
	require.Error(t, err)
	require.ErrorContains(t, input.RawCause(err), "网关客户端未配置")
}
