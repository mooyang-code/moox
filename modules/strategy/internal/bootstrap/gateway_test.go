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
	"net/http"
	"os"
	"path/filepath"
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
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// strategyGatewayWire 模拟主机网关背后的 Storage、Factor 与 Trade：校验调用方签名、服务目录里的 ACL 与 Storage 角色鉴权。
type strategyGatewayWire struct {
	factorpb.UnimplementedFactorMgr
	tradepb.UnimplementedTradeConsoleService
	storagepb.UnimplementedMetadata
	storagepb.UnimplementedDataView
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	directory   servicecatalog.Directory
	catalog     servicecatalog.Catalog
	mu          sync.Mutex
	nonces      map[string]bool
	calls       map[string]int
	refreshes   int
	owner       *tradepb.LogicalAccount
}

func (w *strategyGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == w.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: w.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: w.directory.Version,
		Services: map[string]*directorypb.ServiceHosts{
			"trpc.moox.trade.TradeConsoleService": {HostIds: []string{"control"}},
			"trpc.moox.factor.FactorMgr":          {HostIds: []string{"control"}},
			"trpc.moox.storage.Metadata":          {HostIds: []string{"control"}},
			"trpc.moox.storage.DataView":          {HostIds: []string{"control"}},
		}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}

func (w *strategyGatewayWire) verify(ctx context.Context, service, method string, request proto.Message) (gatewayauth.Claims, error) {
	if !w.catalog.Allowed("strategy", service, method) {
		return gatewayauth.Claims{}, fmt.Errorf("strategy 调用 %s/%s 不在服务目录的 ACL 里", service, method)
	}
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return gatewayauth.Claims{}, err
	}
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method,
		TargetNode: "control", Callee: service, Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return gatewayauth.Claims{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nonces[claims.Nonce] {
		return gatewayauth.Claims{}, errors.New("strategy 签名 nonce 被重放")
	}
	w.nonces[claims.Nonce] = true
	w.calls[service+"/"+method]++
	return claims, nil
}

type strategyStorageRequest interface {
	proto.Message
	GetAuthInfo() *commonpb.AuthInfo
	GetSpaceId() string
}

func (w *strategyGatewayWire) verifyStorage(ctx context.Context, service, method string, req strategyStorageRequest) error {
	if _, err := w.verify(ctx, "trpc.moox.storage."+service, method, req); err != nil {
		return err
	}
	key := "fixture-primary-role-key"
	if service == "DataView" {
		key = "fixture-view-role-key"
	}
	if req.GetSpaceId() != "crypto" || !proto.Equal(req.GetAuthInfo(), &commonpb.AuthInfo{AppId: "strategy", AppKey: key, Operator: "strategy"}) {
		return errors.New("strategy 的 Storage 空间或角色鉴权发生了变化")
	}
	return nil
}

func (w *strategyGatewayWire) GetFactor(ctx context.Context, req *factorpb.GetFactorReq) (*factorpb.GetFactorRsp, error) {
	if _, err := w.verify(ctx, "trpc.moox.factor.FactorMgr", "GetFactor", req); err != nil {
		return nil, err
	}
	return &factorpb.GetFactorRsp{RetInfo: &commonpb.RetInfo{}, Factor: &factorpb.FactorDef{FactorId: req.GetFactorId(), DefinitionHash: "sha256:fixture"}}, nil
}

func (w *strategyGatewayWire) GetView(ctx context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "GetView", req); err != nil {
		return nil, err
	}
	return &storagepb.GetViewRsp{RetInfo: &commonpb.RetInfo{}, View: &storagepb.View{ViewId: req.GetViewId(), DatasetId: "bars", Status: "active", Freq: "1m", ActiveIndexId: "index-1"}}, nil
}

func (w *strategyGatewayWire) ListViewColumns(ctx context.Context, req *storagepb.ListViewColumnsReq) (*storagepb.ListViewColumnsRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListViewColumns", req); err != nil {
		return nil, err
	}
	return &storagepb.ListViewColumnsRsp{RetInfo: &commonpb.RetInfo{}, Columns: []*storagepb.ViewColumn{{ColumnName: "close"}}}, nil
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
	return &storagepb.GetTagRsp{RetInfo: &commonpb.RetInfo{}, Tag: &storagepb.Tag{TagId: req.GetTagId(), MarketType: "spot"}}, nil
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

func (w *strategyGatewayWire) QueryTimeSeriesRows(ctx context.Context, req *storagepb.QueryTimeSeriesRowsReq) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	if err := w.verifyStorage(ctx, "DataView", "QueryTimeSeriesRows", req); err != nil {
		return nil, err
	}
	if req.GetExpectedActiveIndexId() != "index-1" || len(req.GetSelectors()) != 1 || req.GetSelectors()[0].GetSubjectId() != "BTC" {
		return nil, errors.New("Storage 的 View 索引或选择器发生了变化")
	}
	return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, Complete: true, ServedActiveIndexId: "index-1", ServedActiveIndexRevision: 7,
		Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SubjectId: "BTC", DataTime: req.GetTimeRange().GetStartTime()}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "123.45"}}}}}}}, nil
}

// verifyTrade 校验 Trade 调用：签名、ACL，以及网关转发的可信空间。
func (w *strategyGatewayWire) verifyTrade(ctx context.Context, method string, req proto.Message) error {
	if _, err := w.verify(ctx, "trpc.moox.trade.TradeConsoleService", method, req); err != nil {
		return err
	}
	if space := string(codec.Message(ctx).ServerMetaData()["X-Space-Id"]); space != "crypto" {
		return fmt.Errorf("Trade 调用缺少可信空间：%q", space)
	}
	return nil
}

func (w *strategyGatewayWire) account() *tradepb.LogicalAccount {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.owner == nil {
		w.owner = &tradepb.LogicalAccount{LogicalAccountId: "acct-1", SpaceId: "crypto", AuthFence: "fence-1"}
	}
	return proto.Clone(w.owner).(*tradepb.LogicalAccount)
}

func (w *strategyGatewayWire) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq) (*tradepb.GetLogicalAccountRsp, error) {
	if err := w.verifyTrade(ctx, "GetLogicalAccount", req); err != nil {
		return nil, err
	}
	return &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: w.account()}, nil
}

func (w *strategyGatewayWire) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	if err := w.verifyTrade(ctx, "ClaimLogicalAccountOwner", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.owner = &tradepb.LogicalAccount{LogicalAccountId: req.GetLogicalAccountId(), SpaceId: "crypto", AuthFence: "fence-1", OwnerInstanceId: req.GetInstanceId(), OwnerSessionId: req.GetSessionId()}
	owner := proto.Clone(w.owner).(*tradepb.LogicalAccount)
	w.mu.Unlock()
	return &tradepb.ClaimLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: owner}, nil
}

func (w *strategyGatewayWire) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	if err := w.verifyTrade(ctx, "ReleaseLogicalAccountOwner", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.owner = &tradepb.LogicalAccount{LogicalAccountId: req.GetLogicalAccountId(), SpaceId: "crypto", AuthFence: "fence-1"}
	owner := proto.Clone(w.owner).(*tradepb.LogicalAccount)
	w.mu.Unlock()
	return &tradepb.ReleaseLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: owner}, nil
}

func strategyGatewayFixture(t *testing.T, natsURL string) (Config, *strategyGatewayWire) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "strategy", "config", "app.yaml")
	hostPath := filepath.Join(root, "host-gateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-strategy.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := gatewayauth.Credentials{Caller: "strategy", KeyID: "admin-assigned-strategy-key-48", Secret: "strategy-signing-fixture-secret-at-least-32-bytes"}
	require.NoError(t, os.WriteFile(keyPath, []byte(credentials.Secret+"\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host := hostgatewayconfig.Default("control", "control", "192.0.2.1", "host-fixture-key")
	host.Server.LocalAddr = listener.Addr().String()
	host.TLS.CAFile = caPath
	encoded, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostPath, encoded, 0o600))
	directory := servicecatalog.Directory{Services: map[string][]string{
		"trpc.moox.trade.TradeConsoleService": {"control"}, "trpc.moox.factor.FactorMgr": {"control"}, "trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.DataView": {"control"},
	}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	wire := &strategyGatewayWire{credentials: credentials, directory: directory, catalog: catalog, nonces: map[string]bool{}, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	factorpb.RegisterFactorMgrService(svc, wire)
	tradepb.RegisterTradeConsoleServiceService(svc, wire)
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterDataViewService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	raw := fmt.Sprintf("database: %q\ngateway_client:\n  caller: strategy\n  key_id: %s\n  key_file: ../../secrets/caller-strategy.key\nstorage:\n  app_id: strategy\n  app_key: fixture-primary-role-key\n  view_app_key: fixture-view-role-key\n", filepath.Join(root, "data", "strategy", "strategy.db"), credentials.KeyID)
	if natsURL != "" {
		raw += fmt.Sprintf("eventbus:\n  urls: [%q]\n", natsURL)
	}
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := Load(configPath)
	require.NoError(t, err)
	return cfg, wire
}

// Storage、Factor 与 Trade 的全部调用都经共享的部署网关：签名、服务目录的 ACL、Storage 角色鉴权与可信空间都由网关桩校验。
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
	view, err := reader.GetView(ctx, "crypto", "source")
	require.NoError(t, err)
	require.Equal(t, "bars", view.DatasetID)
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
	rows, revision, err := reader.QueryRows(ctx, "crypto", input.Query{ViewID: "source", DatasetID: "bars", Frequency: "1m", Subjects: subjects, Start: time.Unix(60, 0), End: time.Unix(61, 0), Columns: []string{"close"}, ExpectedIndexID: "index-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(7), revision)
	require.Equal(t, 123.45, rows[0].Values["close"])
	factor, err := reader.GetFactor(ctx, "momentum")
	require.NoError(t, err)
	require.Equal(t, "sha256:fixture", factor.DefinitionHash)

	owner := tradeowner.New(cfg.Trade, gateway)
	require.NoError(t, owner.ClaimSession(ctx, "crypto", "acct-1", "instance-1", "session-1"))
	require.NoError(t, owner.ValidateSession(ctx, "crypto", "acct-1", "instance-1", "session-1"))
	require.NoError(t, owner.ReleaseSession(ctx, "crypto", "acct-1", "instance-1", "session-1"))

	// 网关调用的参数由 ctx 与服务目录决定，不接受 tRPC 客户端选项。
	_, err = reader.Metadata.GetView(ctx, &storagepb.GetViewReq{}, client.WithTarget("ip://192.0.2.99:20100"))
	require.ErrorContains(t, err, "不接受 tRPC 客户端选项")
	_, err = reader.DataView.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{}, client.WithTimeout(time.Second))
	require.ErrorContains(t, err, "不接受 tRPC 客户端选项")

	wire.mu.Lock()
	calls, refreshes := maps.Clone(wire.calls), wire.refreshes
	wire.mu.Unlock()
	for _, want := range []string{
		"trpc.moox.storage.Metadata/GetView", "trpc.moox.storage.Metadata/ListViewColumns", "trpc.moox.storage.Metadata/GetDataset",
		"trpc.moox.storage.Metadata/GetTag", "trpc.moox.storage.Metadata/ListDatasetSubjects", "trpc.moox.storage.Metadata/ListSubjects",
		"trpc.moox.storage.Metadata/ListTagMembers", "trpc.moox.storage.DataView/QueryTimeSeriesRows", "trpc.moox.factor.FactorMgr/GetFactor",
		"trpc.moox.trade.TradeConsoleService/GetLogicalAccount", "trpc.moox.trade.TradeConsoleService/ClaimLogicalAccountOwner",
		"trpc.moox.trade.TradeConsoleService/ReleaseLogicalAccountOwner",
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
