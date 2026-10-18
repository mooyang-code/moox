package bootstrap

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
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
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
	credentials gatewayauth.Credentials
	directory   servicecatalog.Directory
	mu          sync.Mutex
	nonces      map[string]bool
	reads       int
	writes      int
	refreshes   int
}

func (w *factorGatewayWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	w.refreshes++
	w.mu.Unlock()
	if req.GetCurrentVersion() == w.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: w.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: w.directory.Version,
		Services: map[string]*directorypb.ServiceHosts{
			"trpc.moox.storage.Metadata":     {HostIds: []string{"control"}},
			"trpc.moox.storage.PrimaryStore": {HostIds: []string{"control"}},
		}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}

func (w *factorGatewayWire) verify(ctx context.Context, service, method string, request proto.Message) error {
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
		return errors.New("factor-mgr call reused a signing nonce")
	}
	w.nonces[claims.Nonce] = true
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
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: &storagepb.Dataset{SpaceId: "crypto", DatasetId: "bars", Status: "disabled", Revision: 7}}, nil
}

func (w *factorGatewayWire) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.Metadata", "ActivateDataset", req); err != nil {
		return nil, err
	}
	if req.GetExpectedRevision() != 7 || req.GetSpaceId() != "crypto" || req.GetDatasetId() != "bars" || !proto.Equal(req.GetAuthInfo(), storageio.AuthInfo("fixture-factor-request")) {
		return nil, errors.New("dataset activation body changed")
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "activation must not retry")
}

func (w *factorGatewayWire) ReportFactorPeriodComputed(ctx context.Context, req *storagepb.ReportFactorPeriodComputedReq) (*storagepb.ReportFactorPeriodComputedRsp, error) {
	if err := w.verify(ctx, "trpc.moox.storage.PrimaryStore", "ReportFactorPeriodComputed", req); err != nil {
		return nil, err
	}
	if !proto.Equal(req.GetAuthInfo(), storageio.AuthInfo("fixture-factor-request")) {
		return nil, errors.New("Storage role authentication changed")
	}
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "writes must not retry")
}

func TestFactorStorageUsesDeploymentIdentityAndClosesOwnedClient(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "fixture-storage-primary-role-secret")
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET", "ip://192.0.2.99:11003")
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID", "wrong-host")
	t.Setenv("MOOX_FACTOR_STORAGE_KEY_ID", "obsolete-key")
	t.Setenv("MOOX_FACTOR_STORAGE_HMAC_KEY_FILE", "/nonexistent/obsolete-factor.key")
	root := t.TempDir()
	configPath := filepath.Join(root, "factor-mgr", "config", "app.yaml")
	hostPath := filepath.Join(root, "host-gateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-factor-mgr.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	credentials := gatewayauth.Credentials{Caller: "factor-mgr", KeyID: "admin-assigned-factor-mgr-key-97", Secret: "factor-mgr-signing-fixture-key-at-least-32-bytes"}
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
		"trpc.moox.storage.Metadata": {"control"}, "trpc.moox.storage.PrimaryStore": {"control"},
	}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	wire := &factorGatewayWire{credentials: credentials, directory: directory, nonces: map[string]bool{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	storagepb.RegisterMetadataService(svc, wire)
	storagepb.RegisterPrimaryStoreService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "factor-mgr")
	raw := "database:\n  path: " + filepath.Join(data, "factor-mgr.db") + "\ngateway_client:\n  caller: factor-mgr\n  key_id: " + credentials.KeyID + "\n  key_file: ../../secrets/caller-factor-mgr.key\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0o600))
	cfg, err := Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	runtime := &Runtime{gateway: gateway}
	storage := storageio.NewGatewayClient(gateway, storageio.AuthInfo("fixture-factor-request"))
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	rsp, err := storage.GetDataset(t.Context(), "crypto", "bars")
	require.NoError(t, err)
	require.Equal(t, "bars", rsp.DatasetID)
	err = storage.ReportComputed(t.Context(), storageio.PeriodMarker{SpaceID: "crypto", ResultDatasetID: "factor-result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 60, TriggerEventID: "fixture-period"})
	require.ErrorContains(t, err, "writes must not retry")
	err = storage.ActivateDataset(t.Context(), "crypto", "bars")
	require.ErrorContains(t, err, "activation must not retry")
	wire.mu.Lock()
	require.Equal(t, 3, wire.reads)
	require.Equal(t, 2, wire.writes)
	require.Len(t, wire.nonces, 5)
	require.GreaterOrEqual(t, wire.refreshes, 2)
	wire.mu.Unlock()
	cache, err := os.Stat(filepath.Join(data, "gatewayclient", "directory.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), cache.Mode().Perm())
	require.NoError(t, runtime.Close())
	require.NoError(t, runtime.Close())
	_, err = storage.GetDataset(t.Context(), "crypto", "bars")
	require.Error(t, err)
	_, err = storageio.NewGatewayClient(nil, nil).GetDataset(t.Context(), "crypto", "bars")
	require.ErrorContains(t, err, "gateway client is required")
}
