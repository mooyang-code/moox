package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
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

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/deployment"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/storagegateway"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	directorypb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type monitorGatewayWire struct {
	storagepb.UnimplementedMetadata
	collectorpb.UnimplementedCollectMgr
	storagepb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	mu        sync.Mutex
	calls     int
	reads     int
	refreshes int
	// statusChanges 记下到达本机 SysDeploy 的 SyncTopology 次数。
	statusChanges int
}

const monitorDirectoryVersion = "v1"

func (w *monitorGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == monitorDirectoryVersion {
		return &directorypb.GetDirectoryRsp{Version: monitorDirectoryVersion}, nil
	}
	// 目录只有主机与部署：Monitor 与 Admin、Collector、Storage 同机。
	rsp := &directorypb.GetDirectoryRsp{Changed: true, Version: monitorDirectoryVersion,
		Hosts: []*directorypb.DirectoryHost{{HostId: "control", Address: "control.example.test", Reachability: "direct", Enabled: true}}}
	for _, component := range []string{"admin", "collector", "monitor", "storage-primary"} {
		rsp.Deployments = append(rsp.Deployments, &directorypb.DirectoryDeployment{HostId: "control", ComponentId: component, Enabled: true})
	}
	return rsp, nil
}

// verify 核对同机直连的请求不带任何签名头。
func (w *monitorGatewayWire) verify(ctx context.Context, _, _ string, _ proto.Message) error {
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

func (w *monitorGatewayWire) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetDataset", req); err != nil {
		return nil, err
	}
	if req.GetSpaceId() != "crypto" {
		return nil, errors.New("monitor space changed")
	}
	w.mu.Lock()
	w.reads++
	reads := w.reads
	w.mu.Unlock()
	if reads == 1 {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh this read")
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: &storagepb.Dataset{SpaceId: "crypto", DatasetId: req.GetDatasetId()}}, nil
}

func (w *monitorGatewayWire) ReadRows(ctx context.Context, req *storagepb.PrimaryReadRowsReq) (*storagepb.PrimaryReadRowsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReadRows", req); err != nil {
		return nil, err
	}
	return &storagepb.PrimaryReadRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (w *monitorGatewayWire) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq) (*collectorpb.GetTaskResultInventoryRsp, error) {
	if err := w.verify(ctx, "trpc.moox.collector.CollectMgr", "GetTaskResultInventory", req); err != nil {
		return nil, err
	}
	if req.GetSpaceId() != "crypto" || req.GetSnapshotId() != "generation-1" {
		return nil, errors.New("inventory scope changed")
	}
	return &collectorpb.GetTaskResultInventoryRsp{SnapshotId: "generation-1"}, nil
}

func TestMonitorStorageUsesDeploymentIdentityAndClosesOwnedClient(t *testing.T) {
	t.Setenv("MOOX_MONITOR_STORAGE_GATEWAY_TARGET", "ip://192.0.2.99:11003")
	t.Setenv("MOOX_MONITOR_STORAGE_GATEWAY_NODE_ID", "wrong-host")
	root := t.TempDir()
	configPath := filepath.Join(root, "monitor", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "host.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := authtest.Component("monitor", "control")
	require.NoError(t, os.WriteFile(keyPath, []byte(gatewayauth.EncodeSigningKey(credentials.Key)+"\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	wire := &monitorGatewayWire{}
	// Monitor 与这些服务同机：直连服务目录里各服务的回环端口，这里都指到同一个假服务上。
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	servicePorts := map[int]bool{}
	for _, path := range []string{"trpc.moox.storage.Metadata", "trpc.moox.storage.PrimaryStore", "trpc.moox.collector.CollectMgr", "trpc.moox.ops.SysDeploy"} {
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
	collectorpb.RegisterCollectMgrService(svc, wire)
	adminpb.RegisterSysDeployService(svc, &monitorSysDeployWire{wire: wire})
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "monitor")
	raw := "deployment:\n  enabled: false\ndatabase:\n  path: " + filepath.Join(data, "monitor.db") + "\ngateway_client:\n  caller: monitor\n  key_file: ../../secrets/host.key\n  host_id: control\n  local_address: " + listener.Addr().String() + "\n  ca_file: " + caPath + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	runtime := &Runtime{Gateway: gateway, StorageGateway: storagegateway.New(gateway)}
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rsp, err := runtime.StorageGateway.GetDataset(t.Context(), &storagepb.GetDatasetReq{SpaceId: "crypto", DatasetId: "dataset_crypto_kline_1m"})
	require.NoError(t, err)
	require.Equal(t, "crypto", rsp.GetDataset().GetSpaceId())
	rows, err := runtime.StorageGateway.ReadRows(t.Context(), &storagepb.PrimaryReadRowsReq{})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, rows.GetRetInfo().GetCode())
	inventory, err := monmetrics.NewCollectorInventoryGatewayClient(runtime.Gateway)
	require.NoError(t, err)
	inventoryResponse, err := inventory.GetTaskResultInventory(t.Context(), &collectorpb.GetTaskResultInventoryReq{SpaceId: "crypto", SnapshotId: "generation-1"})
	require.NoError(t, err)
	require.Equal(t, "generation-1", inventoryResponse.GetSnapshotId())
	source := deployment.NewClientSource(runtime.Gateway)
	snapshot, err := source.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Deployments, 1)
	require.Equal(t, "control.example.test", snapshot.Hosts[0].GetAddress())
	// Monitor 不在 SyncTopology 的调用方名单里。同机直连不查名单（名单由网关在跨主机调用时把关），请求会到达本机服务。
	embedded, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.False(t, embedded.Allowed("monitor", "trpc.moox.ops.SysDeploy", "SyncTopology"))
	var unlisted adminpb.SyncTopologyRsp
	require.NoError(t, runtime.Gateway.Invoke(t.Context(), "trpc.moox.ops.SysDeploy", "SyncTopology", &adminpb.SyncTopologyReq{}, &unlisted))
	wire.mu.Lock()
	reads, calls, refreshes, statusChanges := wire.reads, wire.calls, wire.refreshes, wire.statusChanges
	wire.mu.Unlock()
	require.Equal(t, 2, reads)
	require.Equal(t, 8, calls)
	require.Equal(t, 1, statusChanges)
	require.GreaterOrEqual(t, refreshes, 2)
	_, err = runtime.StorageGateway.GetDataset(t.Context(), &storagepb.GetDatasetReq{SpaceId: "crypto"}, client.WithTarget("ip://192.0.2.99:20200"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	cache, err := os.Stat(filepath.Join(data, "gatewayclient", "directory.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), cache.Mode().Perm())
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	_, err = runtime.StorageGateway.GetDataset(t.Context(), &storagepb.GetDatasetReq{SpaceId: "crypto"})
	require.Error(t, err)
	_, err = storagegateway.New(nil).GetDataset(t.Context(), &storagepb.GetDatasetReq{})
	require.ErrorContains(t, err, "unavailable")
}

type monitorSysDeployWire struct {
	adminpb.UnimplementedSysDeploy
	wire *monitorGatewayWire
}

func (w monitorSysDeployWire) SyncTopology(ctx context.Context, req *adminpb.SyncTopologyReq) (*adminpb.SyncTopologyRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "SyncTopology", req); err != nil {
		return nil, err
	}
	w.wire.mu.Lock()
	w.wire.statusChanges++
	w.wire.mu.Unlock()
	return &adminpb.SyncTopologyRsp{RetInfo: &adminpb.RetInfo{}}, nil
}

func (w monitorSysDeployWire) GetCatalog(ctx context.Context, req *adminpb.GetCatalogReq) (*adminpb.GetCatalogRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "GetCatalog", req); err != nil {
		return nil, err
	}
	raw := servicecatalog.EmbeddedYAML()
	hash := sha256.Sum256(raw)
	return &adminpb.GetCatalogRsp{RetInfo: &adminpb.RetInfo{}, CatalogYaml: string(raw), Sha256: hex.EncodeToString(hash[:])}, nil
}

func (w monitorSysDeployWire) GetHostRoutes(ctx context.Context, req *adminpb.GetHostRoutesReq) (*adminpb.GetHostRoutesRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "GetHostRoutes", req); err != nil {
		return nil, err
	}
	return &adminpb.GetHostRoutesRsp{RetInfo: &adminpb.RetInfo{}, HostId: req.GetHostId()}, nil
}
func (w monitorSysDeployWire) ListHosts(ctx context.Context, req *adminpb.ListDeploymentHostsReq) (*adminpb.ListDeploymentHostsRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", req); err != nil {
		return nil, err
	}
	return &adminpb.ListDeploymentHostsRsp{RetInfo: &adminpb.RetInfo{}, Hosts: []*adminpb.DeploymentHost{{HostId: "control", Address: "control.example.test", Status: "enabled"}}}, nil
}
func (w monitorSysDeployWire) ListDeployments(ctx context.Context, req *adminpb.ListDeploymentsReq) (*adminpb.ListDeploymentsRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "ListDeployments", req); err != nil {
		return nil, err
	}
	return &adminpb.ListDeploymentsRsp{RetInfo: &adminpb.RetInfo{}, Deployments: []*adminpb.ComponentDeployment{{HostId: "control", ComponentId: "admin", Status: "enabled"}}}, nil
}
