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
	"github.com/mooyang-code/moox/modules/strategy/internal/factorio"
	"github.com/mooyang-code/moox/modules/strategy/internal/storageio"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type strategyGatewayWire struct {
	factorpb.UnimplementedFactorMgr
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
			"trpc.moox.factor.FactorMgr": {HostIds: []string{"control"}},
			"trpc.moox.storage.Metadata": {HostIds: []string{"control"}},
			"trpc.moox.storage.DataView": {HostIds: []string{"control"}},
		}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}

func (w *strategyGatewayWire) verify(ctx context.Context, service, method string, request proto.Message) error {
	if !w.catalog.Allowed("strategy", service, method) {
		return errors.New("strategy method absent from the catalog ACL")
	}
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method,
		TargetNode: "control", Callee: service, Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nonces[claims.Nonce] {
		return errors.New("strategy signing nonce reused")
	}
	w.nonces[claims.Nonce] = true
	w.calls[service+"/"+method]++
	return nil
}

type strategyStorageRequest interface {
	proto.Message
	GetAuthInfo() *commonpb.AuthInfo
	GetSpaceId() string
}

func (w *strategyGatewayWire) verifyStorage(ctx context.Context, service, method string, req strategyStorageRequest) error {
	if err := w.verify(ctx, "trpc.moox.storage."+service, method, req); err != nil {
		return err
	}
	key := "fixture-primary-role-key"
	if service == "DataView" {
		key = "fixture-view-role-key"
	}
	if req.GetSpaceId() != "crypto" || !proto.Equal(req.GetAuthInfo(), &commonpb.AuthInfo{AppId: "strategy", AppKey: key, Operator: "strategy"}) {
		return errors.New("strategy Storage space or role authentication changed")
	}
	return nil
}

func (w *strategyGatewayWire) ListFactorSets(ctx context.Context, req *factorpb.ListFactorSetsReq) (*factorpb.ListFactorSetsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.factor.FactorMgr", "ListFactorSets", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	first := w.calls["trpc.moox.factor.FactorMgr/ListFactorSets"] == 1
	w.mu.Unlock()
	if first {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh this read")
	}
	return &factorpb.ListFactorSetsRsp{RetInfo: &commonpb.RetInfo{}, FactorSets: []*factorpb.FactorSetInfo{{FactorSet: &factorpb.FactorSet{SetId: "set-1", Status: "enabled", ResultDatasetId: "factor-results", SourceDatasetId: "bars", Freq: "1m"}}}}, nil
}

func (w *strategyGatewayWire) ListFactors(ctx context.Context, req *factorpb.ListFactorsReq) (*factorpb.ListFactorsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.factor.FactorMgr", "ListFactors", req); err != nil {
		return nil, err
	}
	if req.GetSetId() != "set-1" || req.GetIncludeSource() {
		return nil, errors.New("factor catalog request changed")
	}
	return &factorpb.ListFactorsRsp{RetInfo: &commonpb.RetInfo{}, Factors: []*factorpb.FactorInfo{{Factor: &factorpb.FactorDef{FactorId: "momentum", Outputs: []string{"bias"}, SourceHash: "source-hash"}, Usages: []*factorpb.FactorUsage{{SetId: "set-1", Status: "enabled"}}}}}, nil
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

func (w *strategyGatewayWire) ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "ListDatasetSubjects", req); err != nil {
		return nil, err
	}
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{}, DatasetSubjects: []*storagepb.DatasetSubject{{SpaceId: "crypto", DatasetId: "bars", SubjectId: "BTC", Status: "active"}}}, nil
}

func (w *strategyGatewayWire) GetSubject(ctx context.Context, req *storagepb.GetSubjectReq) (*storagepb.GetSubjectRsp, error) {
	if err := w.verifyStorage(ctx, "Metadata", "GetSubject", req); err != nil {
		return nil, err
	}
	return &storagepb.GetSubjectRsp{RetInfo: &commonpb.RetInfo{}, Subject: &storagepb.Subject{SubjectId: req.GetSubjectId(), Status: "active"}}, nil
}

func (w *strategyGatewayWire) QueryTimeSeriesRows(ctx context.Context, req *storagepb.QueryTimeSeriesRowsReq) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	if err := w.verifyStorage(ctx, "DataView", "QueryTimeSeriesRows", req); err != nil {
		return nil, err
	}
	if req.GetExpectedActiveIndexId() != "index-1" || len(req.GetSelectors()) != 1 || req.GetSelectors()[0].GetSubjectId() != "BTC" || req.GetSelectors()[0].GetFreq() != "1m" {
		return nil, errors.New("Storage view index or selectors changed")
	}
	return &storagepb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, Complete: true, ServedActiveIndexId: "index-1", ServedActiveIndexRevision: 7,
		Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SubjectId: "BTC", DataTime: req.GetTimeRange().GetStartTime()}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "123.45"}}}}}}}, nil
}

func strategyGatewayFixture(t *testing.T, natsURL string) (Config, *strategyGatewayWire) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "strategy", "config", "app.yaml")
	hostPath := filepath.Join(root, "hostgateway", "config", "app.yaml")
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
		"trpc.moox.factor.FactorMgr": {"control"}, "trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.DataView": {"control"},
	}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	wire := &strategyGatewayWire{credentials: credentials, directory: directory, catalog: catalog, nonces: map[string]bool{}, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	factorpb.RegisterFactorMgrService(svc, wire)
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

func TestStrategyDependenciesUseSharedDeploymentGateway(t *testing.T) {
	for _, name := range []string{"MOOX_SERVICE_GATEWAY_TARGET", "MOOX_LOCAL_STORAGE_RPC_GATEWAY_TARGET"} {
		t.Setenv(name, "ip://192.0.2.99:11003")
	}
	for _, name := range []string{"MOOX_GATEWAY_TARGET_NODE", "MOOX_LOCAL_STORAGE_GATEWAY_NODE_ID"} {
		t.Setenv(name, "wrong-host")
	}
	cfg, wire := strategyGatewayFixture(t, "")
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	selected := newCompilerFactory(cfg, gateway)("crypto")
	sets, err := selected.Factors.ListFactorSets(t.Context())
	require.NoError(t, err)
	require.Len(t, sets, 1)
	factors, err := selected.Factors.ListFactors(t.Context(), sets[0])
	require.NoError(t, err)
	require.Len(t, factors, 1)
	require.Equal(t, "source-hash", factors[0].SourceHash)
	view, err := selected.Storage.GetView(t.Context(), "source")
	require.NoError(t, err)
	require.Equal(t, "bars", view.DatasetID)
	columns, err := selected.Storage.ListViewColumns(t.Context(), "source")
	require.NoError(t, err)
	require.Len(t, columns, 1)
	require.Equal(t, "close", columns[0].Name)
	reader := newStorageReader(cfg, gateway)
	subjects, err := reader.ListSubjects(t.Context(), "crypto", "source")
	require.NoError(t, err)
	require.Len(t, subjects, 1)
	require.Equal(t, "BTC", subjects[0].InstrumentID)
	rows, err := reader.ReadPeriod(t.Context(), "crypto", "source", time.Unix(60, 0))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "123.45", rows[0].Values["close"])
	_, err = selected.Factors.(*factorio.RPCClient).Proxy.ListFactorSets(t.Context(), &factorpb.ListFactorSetsReq{}, client.WithTarget("ip://192.0.2.99:11400"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	_, err = reader.Metadata.GetView(t.Context(), &storagepb.GetViewReq{}, client.WithTarget("ip://192.0.2.99:20100"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	_, err = reader.DataView.QueryTimeSeriesRows(t.Context(), &storagepb.QueryTimeSeriesRowsReq{}, client.WithTimeout(time.Second))
	require.ErrorContains(t, err, "instead of tRPC client options")
	wire.mu.Lock()
	calls, refreshes := maps.Clone(wire.calls), wire.refreshes
	wire.mu.Unlock()
	require.Len(t, calls, 7)
	require.Equal(t, 2, calls["trpc.moox.factor.FactorMgr/ListFactorSets"])
	require.GreaterOrEqual(t, refreshes, 2)
	cache, err := os.Stat(filepath.Join(filepath.Dir(cfg.Database), "gatewayclient", "directory.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), cache.Mode().Perm())
	require.NoError(t, gateway.Close())
	_, err = selected.Factors.ListFactorSets(t.Context())
	require.Error(t, err)
	_, err = factorio.NewGatewayClient(nil).ListFactorSets(t.Context())
	require.ErrorContains(t, err, "gateway client is required")
	_, err = storageio.NewGatewayClient(nil).GetView(t.Context(), "source")
	require.ErrorContains(t, err, "gateway client is required")
}
