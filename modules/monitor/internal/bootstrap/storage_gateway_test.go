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
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	"github.com/mooyang-code/moox/modules/monitor/internal/storagegateway"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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

type monitorGatewayWire struct {
	storagepb.UnimplementedMetadata
	collectorpb.UnimplementedCollectMgr
	storagepb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	directory   servicecatalog.Directory
	mu          sync.Mutex
	nonces      map[string]bool
	reads       int
	writes      int
	refreshes   int
}

func (w *monitorGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == w.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: w.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: w.directory.Version,
		Services: map[string]*directorypb.ServiceHosts{
			"trpc.moox.collector.CollectMgr": {HostIds: []string{"control"}},
			"trpc.moox.ops.SysDeploy":        {HostIds: []string{"control"}},
			"trpc.moox.storage.Metadata":     {HostIds: []string{"control"}},
			"trpc.moox.storage.PrimaryStore": {HostIds: []string{"control"}},
		}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}

func (w *monitorGatewayWire) verify(ctx context.Context, service, method string, request proto.Message) error {
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
		return errors.New("monitor call reused a signing nonce")
	}
	w.nonces[claims.Nonce] = true
	return nil
}

func (w *monitorGatewayWire) GetSpace(ctx context.Context, req *storagepb.GetSpaceReq) (*storagepb.GetSpaceRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "GetSpace", req); err != nil {
		return nil, err
	}
	if req.GetSpaceId() != "mooxsys" {
		return nil, errors.New("monitor space changed")
	}
	w.mu.Lock()
	w.reads++
	reads := w.reads
	w.mu.Unlock()
	if reads == 1 {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh this read")
	}
	return &storagepb.GetSpaceRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Space: &storagepb.Space{SpaceId: "mooxsys", Status: "active"}}, nil
}

func (w *monitorGatewayWire) UpsertFields(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "UpsertFields", req); err != nil {
		return nil, err
	}
	if !proto.Equal(req.GetAuthInfo(), storageauth.Primary("monitor")) {
		return nil, errors.New("Storage role authentication changed")
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "writes must not retry")
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
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "fixture-storage-primary-role-secret")
	t.Setenv("MOOX_MONITOR_STORAGE_GATEWAY_TARGET", "ip://192.0.2.99:11003")
	t.Setenv("MOOX_MONITOR_STORAGE_GATEWAY_NODE_ID", "wrong-host")
	root := t.TempDir()
	configPath := filepath.Join(root, "monitor", "config", "app.yaml")
	hostPath := filepath.Join(root, "host-gateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-monitor.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := gatewayauth.Credentials{Caller: "monitor", KeyID: "admin-assigned-monitor-key-97", Secret: "monitor-signing-fixture-key-at-least-32-bytes"}
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
		"trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.PrimaryStore": {"control"}, "trpc.moox.collector.CollectMgr": {"control"}, "trpc.moox.ops.SysDeploy": {"control"},
	}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	wire := &monitorGatewayWire{credentials: credentials, directory: directory, nonces: map[string]bool{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	collectorpb.RegisterCollectMgrService(svc, wire)
	adminpb.RegisterSysDeployService(svc, &monitorSysDeployWire{wire: wire})
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "monitor")
	raw := "placement:\n  enabled: false\ndatabase:\n  path: " + filepath.Join(data, "monitor.db") + "\ngateway_client:\n  caller: monitor\n  key_id: " + credentials.KeyID + "\n  key_file: ../../secrets/caller-monitor.key\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	runtime := &Runtime{Gateway: gateway, StorageGateway: storagegateway.New(gateway)}
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rsp, err := runtime.StorageGateway.GetSpace(t.Context(), &storagepb.GetSpaceReq{SpaceId: "mooxsys"})
	require.NoError(t, err)
	require.Equal(t, "mooxsys", rsp.GetSpace().GetSpaceId())
	_, err = runtime.StorageGateway.UpsertFields(t.Context(), &storagepb.PrimaryUpsertFieldsReq{AuthInfo: storageauth.Primary("monitor")})
	require.Equal(t, int(errs.RetServerNoService), int(errs.Code(err)), "%v", err)
	inventory, err := monmetrics.NewCollectorInventoryGatewayClient(runtime.Gateway)
	require.NoError(t, err)
	inventoryResponse, err := inventory.GetTaskResultInventory(t.Context(), &collectorpb.GetTaskResultInventoryReq{SpaceId: "crypto", SnapshotId: "generation-1"})
	require.NoError(t, err)
	require.Equal(t, "generation-1", inventoryResponse.GetSnapshotId())
	source := placement.NewClientSource(runtime.Gateway)
	snapshot, err := source.Snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Placements, 1)
	require.Equal(t, "control.example.test", snapshot.Hosts[0].GetAddress())
	gatewayStatus, err := source.GatewayStatus(t.Context(), "control")
	require.NoError(t, err)
	require.Equal(t, "control-instance", gatewayStatus.GetInstanceId())
	var forbidden adminpb.SetPlacementStatusRsp
	require.Error(t, runtime.Gateway.Invoke(t.Context(), "trpc.moox.ops.SysDeploy", "SetPlacementStatus", &adminpb.SetPlacementStatusReq{}, &forbidden))
	wire.mu.Lock()
	reads, writes, nonces, refreshes := wire.reads, wire.writes, len(wire.nonces), wire.refreshes
	wire.mu.Unlock()
	require.Equal(t, 2, reads)
	require.Equal(t, 1, writes)
	require.Equal(t, 8, nonces)
	require.GreaterOrEqual(t, refreshes, 2)
	_, err = runtime.StorageGateway.GetSpace(t.Context(), &storagepb.GetSpaceReq{SpaceId: "mooxsys"}, client.WithTarget("ip://192.0.2.99:20200"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	cache, err := os.Stat(filepath.Join(data, "gatewayclient", "directory.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), cache.Mode().Perm())
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	_, err = runtime.StorageGateway.GetSpace(t.Context(), &storagepb.GetSpaceReq{SpaceId: "mooxsys"})
	require.Error(t, err)
	_, err = storagegateway.New(nil).GetSpace(t.Context(), &storagepb.GetSpaceReq{})
	require.ErrorContains(t, err, "unavailable")
}

type monitorSysDeployWire struct {
	adminpb.UnimplementedSysDeploy
	wire *monitorGatewayWire
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
	return &adminpb.GetHostRoutesRsp{RetInfo: &adminpb.RetInfo{}, HostId: req.GetHostId(), GatewayStatus: &adminpb.HostGatewayRuntimeStatus{InstanceId: "control-instance"}}, nil
}
func (w monitorSysDeployWire) ListHosts(ctx context.Context, req *adminpb.ListDeploymentHostsReq) (*adminpb.ListDeploymentHostsRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", req); err != nil {
		return nil, err
	}
	return &adminpb.ListDeploymentHostsRsp{RetInfo: &adminpb.RetInfo{}, Hosts: []*adminpb.DeploymentHost{{HostId: "control", Address: "control.example.test", Status: "enabled"}}}, nil
}
func (w monitorSysDeployWire) ListPlacements(ctx context.Context, req *adminpb.ListPlacementsReq) (*adminpb.ListPlacementsRsp, error) {
	if err := w.wire.verify(ctx, "trpc.moox.ops.SysDeploy", "ListPlacements", req); err != nil {
		return nil, err
	}
	return &adminpb.ListPlacementsRsp{RetInfo: &adminpb.RetInfo{}, Placements: []*adminpb.ComponentPlacement{{HostId: "control", ComponentId: "admin", Status: "enabled"}}}, nil
}
