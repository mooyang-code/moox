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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	directorypb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type factorGatewayWire struct {
	storagepb.UnimplementedMetadata
	storagepb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	mu        sync.Mutex
	calls     int
	reads     int
	writes    int
	refreshes int
}

const factorDirectoryVersion = "v1"

func (w *factorGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == factorDirectoryVersion {
		return &directorypb.GetDirectoryRsp{Version: factorDirectoryVersion}, nil
	}
	// 目录只有主机与部署：factor-mgr 与 Storage 同机。
	return &directorypb.GetDirectoryRsp{Changed: true, Version: factorDirectoryVersion,
		Hosts: []*directorypb.DirectoryHost{{HostId: "control", Address: "control.example.test", Reachability: "direct", Enabled: true}},
		Deployments: []*directorypb.DirectoryDeployment{
			{HostId: "control", ComponentId: "factor-mgr", Enabled: true},
			{HostId: "control", ComponentId: "storage-primary", Enabled: true},
		}}, nil
}

// verify 核对同机直连的请求不带任何签名头。
func (w *factorGatewayWire) verify(ctx context.Context, _, _ string, _ proto.Message) error {
	for name := range codec.Message(ctx).ServerMetaData() {
		if strings.HasPrefix(strings.ToLower(name), "x-moox-") {
			return fmt.Errorf("direct local call carries signature header %s", name)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	return nil
}

func (w *factorGatewayWire) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetDataset", req); err != nil {
		return nil, err
	}
	if req.GetSpaceId() != "crypto" || req.GetDatasetId() != "bars" {
		return nil, errors.New("factor-mgr space changed")
	}
	w.mu.Lock()
	w.reads++
	reads := w.reads
	w.mu.Unlock()
	if reads == 1 {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh this read")
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: &storagepb.Dataset{SpaceId: "crypto", DatasetId: "bars", Status: "active", Revision: 7}}, nil
}

func (w *factorGatewayWire) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "UpsertDatasetColumn", req); err != nil {
		return nil, err
	}
	if req.GetColumn().GetSpaceId() != "crypto" || req.GetColumn().GetDatasetId() != "bars" || req.GetColumn().GetColumnName() != "close" {
		return nil, errors.New("dataset column body changed")
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "column upsert must not retry")
}

func (w *factorGatewayWire) ReportDatasetPeriodCompleted(ctx context.Context, req *storagepb.ReportDatasetPeriodCompletedReq) (*storagepb.ReportDatasetPeriodCompletedRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReportDatasetPeriodCompleted", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "writes must not retry")
}

func TestFactorStorageUsesDeploymentIdentityAndClosesOwnedClient(t *testing.T) {
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET", "ip://192.0.2.99:11003")
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID", "wrong-host")
	t.Setenv("MOOX_FACTOR_STORAGE_KEY_ID", "obsolete-key")
	t.Setenv("MOOX_FACTOR_STORAGE_HMAC_KEY_FILE", "/nonexistent/obsolete-factor.key")
	root := t.TempDir()
	configPath := filepath.Join(root, "factor-mgr", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "host.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := authtest.Component("factor-mgr", "control")
	require.NoError(t, os.WriteFile(keyPath, []byte(gatewayauth.EncodeSigningKey(credentials.Key)+"\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	wire := &factorGatewayWire{}
	// factor-mgr 与 Storage 同机：直连服务目录里 Storage 各服务的回环端口，这里都指到同一个假服务上。
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	servicePorts := map[int]bool{}
	for _, path := range []string{"trpc.moox.storage.Metadata", "trpc.moox.storage.PrimaryStore"} {
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
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "factor-mgr")
	raw := "database:\n  path: " + filepath.Join(data, "factor-mgr.db") + "\ngateway_client:\n  caller: factor-mgr\n  key_file: ../../secrets/host.key\n  host_id: control\n  local_address: " + listener.Addr().String() + "\n  ca_file: " + caPath + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	runtime := &Runtime{gateway: gateway}
	storage := storageio.NewGatewayClient(gateway)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rsp, err := storage.GetDataset(t.Context(), "crypto", "bars")
	require.NoError(t, err)
	require.Equal(t, "bars", rsp.DatasetID)
	err = storage.ReportComputed(t.Context(), storageio.PeriodMarker{SpaceID: "crypto", ResultDatasetID: "factor-result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 60, TriggerEventID: "fixture-period"})
	require.ErrorContains(t, err, "writes must not retry")
	err = storage.UpsertColumns(t.Context(), "crypto", "bars", []storageio.ColumnInfo{{ColumnName: "close", OriginType: storageio.ColumnOriginField, ValueType: storageio.ColumnTypeDouble}})
	require.ErrorContains(t, err, "column upsert must not retry")
	wire.mu.Lock()
	require.Equal(t, 2, wire.reads)
	require.Equal(t, 2, wire.writes)
	require.Equal(t, 4, wire.calls)
	require.GreaterOrEqual(t, wire.refreshes, 2)
	wire.mu.Unlock()
	cache, err := os.Stat(filepath.Join(data, "gatewayclient", "directory.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), cache.Mode().Perm())
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	_, err = storage.GetDataset(t.Context(), "crypto", "bars")
	require.Error(t, err)
	_, err = storageio.NewGatewayClient(nil).GetDataset(t.Context(), "crypto", "bars")
	require.ErrorContains(t, err, "gateway client is required")
}
