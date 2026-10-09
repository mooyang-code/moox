package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/publishlease"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type cloudnodeAdminWire struct {
	adminpb.UnimplementedSecretMgr
	adminpb.UnimplementedCollectorPublishLease
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	directory   servicecatalog.Directory
	mu          sync.Mutex
	nonces      map[string]bool
	calls       map[string]int
}

func (w *cloudnodeAdminWire) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	if req.GetCurrentVersion() == w.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: w.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: w.directory.Version, Services: map[string]*directorypb.ServiceHosts{
		"trpc.moox.ops.SecretMgr":               {HostIds: []string{"control"}},
		"trpc.moox.admin.CollectorPublishLease": {HostIds: []string{"control"}},
	}, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}}, nil
}
func (w *cloudnodeAdminWire) verify(ctx context.Context, service, method string, request proto.Message) error {
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	raw, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: "control", Callee: service, Func: method, Body: raw}, headers, time.Now())
	if err != nil {
		return err
	}
	if service == "trpc.moox.admin.CollectorPublishLease" && headers.Get("X-Space-Id") != "crypto" {
		return errors.New("space metadata changed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nonces[claims.Nonce] {
		return errors.New("nonce reused")
	}
	w.nonces[claims.Nonce] = true
	w.calls[method]++
	return nil
}
func TestCloudNodeAdminCallsShareDeploymentIdentityAndShutdown(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "cloudnode", "config", "app.yaml")
	hostPath := filepath.Join(root, "hostgateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-cloudnode.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	}
	credentials := gatewayauth.Credentials{Caller: "cloudnode", KeyID: "admin-assigned-cloudnode-key-42", Secret: "cloudnode-fixture-signing-key-at-least-32-bytes"}
	require.NoError(t, os.WriteFile(keyPath, []byte(credentials.Secret+"\n"), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host := hostgatewayconfig.Default("control", "control", "192.0.2.1", "host-fixture-key")
	host.Server.LocalAddr = listener.Addr().String()
	host.TLS.CAFile = caPath
	encoded, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostPath, encoded, 0600))
	directory := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.ops.SecretMgr": {"control"}, "trpc.moox.admin.CollectorPublishLease": {"control"}}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	wire := &cloudnodeAdminWire{credentials: credentials, directory: directory, nonces: map[string]bool{}, calls: map[string]int{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	adminpb.RegisterSecretMgrService(svc, wire)
	adminpb.RegisterCollectorPublishLeaseService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "cloudnode")
	raw := "database:\n  path: " + filepath.Join(data, "cloudnode.db") + "\ngateway_client:\n  caller: cloudnode\n  key_id: " + credentials.KeyID + "\n  key_file: ../../secrets/caller-cloudnode.key\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0600))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)

	runtime := &Runtime{Gateway: gateway}
	t.Cleanup(func() { require.NoError(t, runtime.Close(t.Context())) })
	resolver, err := cloudcredential.New(gateway)
	require.NoError(t, err)
	for range 2 {
		secret, err := resolver.Resolve(t.Context(), store.CloudAccount{Provider: "tencent", CredentialSecretID: "secret-1"})
		require.NoError(t, err)
		require.Equal(t, "secret-id", secret.SecretID)
	}
	leaseClient := publishlease.New(gateway)
	lease, err := leaseClient.AcquireLease(t.Context(), "crypto", "holder-1", 0)
	require.NoError(t, err)
	require.NoError(t, leaseClient.Validate(t.Context(), "crypto", lease.LeaseID, lease.FencingToken))
	require.NoError(t, leaseClient.BeginOperation(t.Context(), "crypto", lease.LeaseID, lease.FencingToken, "operation-1"))
	require.NoError(t, leaseClient.RenewOperation(t.Context(), "crypto", "operation-1", lease.FencingToken))
	require.NoError(t, leaseClient.EndOperation(t.Context(), "crypto", "operation-1", lease.FencingToken))
	require.NoError(t, leaseClient.RenewLease(t.Context(), lease))
	require.NoError(t, leaseClient.ReleaseLease(t.Context(), lease))
	var forbidden adminpb.CreateSecretRsp
	require.Error(t, gateway.Invoke(t.Context(), "trpc.moox.ops.SecretMgr", "CreateSecret", &adminpb.CreateSecretReq{}, &forbidden))
	wire.mu.Lock()
	require.Len(t, wire.nonces, 9)
	require.Len(t, wire.calls, 8)
	require.Equal(t, 2, wire.calls["GetSecretValue"])
	wire.mu.Unlock()
	require.NoError(t, runtime.Close(t.Context()))
	require.NoError(t, runtime.Close(t.Context()))
	_, err = resolver.Resolve(t.Context(), store.CloudAccount{Provider: "tencent", CredentialSecretID: "secret-1"})
	require.Error(t, err)
}
func TestRuntimeCloseCancelsAndWaitsForNodeBatchBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { <-ctx.Done(); close(stopped) }()
	runtime := &Runtime{NodeBatchCancel: cancel, NodeBatchWait: func() { <-stopped }}
	require.NoError(t, runtime.Close(t.Context()))
	select {
	case <-stopped:
	default:
		t.Fatal("worker still running")
	}
	require.NoError(t, runtime.Close(t.Context()))
}

func (w *cloudnodeAdminWire) GetSecretValue(ctx context.Context, req *adminpb.GetSecretValueReq) (*adminpb.GetSecretValueRsp, error) {
	if err := w.verify(ctx, "trpc.moox.ops.SecretMgr", "GetSecretValue", req); err != nil {
		return nil, err
	}
	return &adminpb.GetSecretValueRsp{RetInfo: &adminpb.RetInfo{}, Secret: &adminpb.SecretMaterial{SecretId: req.GetSecretId(), Category: "cloud", Provider: "tencent", Status: "active", KeyId: "secret-id", SecretValue: "secret-value"}}, nil
}

func (w *cloudnodeAdminWire) AcquireCollectorPublishLease(ctx context.Context, req *adminpb.AcquireCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: "lease-1", FencingToken: 17}, nil
}

func (w *cloudnodeAdminWire) RenewCollectorPublishLease(ctx context.Context, req *adminpb.RenewCollectorPublishLeaseReq) (*adminpb.CollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, SpaceId: req.GetSpaceId(), LeaseId: req.GetLeaseId(), FencingToken: req.GetFencingToken()}, nil
}

func (w *cloudnodeAdminWire) ReleaseCollectorPublishLease(ctx context.Context, req *adminpb.ReleaseCollectorPublishLeaseReq) (*adminpb.ReleaseCollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "ReleaseCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.ReleaseCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Released: true}, nil
}

func (w *cloudnodeAdminWire) ValidateCollectorPublishLease(ctx context.Context, req *adminpb.ValidateCollectorPublishLeaseReq) (*adminpb.ValidateCollectorPublishLeaseRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "ValidateCollectorPublishLease", req); err != nil {
		return nil, err
	}
	return &adminpb.ValidateCollectorPublishLeaseRsp{RetInfo: &adminpb.RetInfo{}, Valid: true, CurrentFencingToken: req.GetFencingToken()}, nil
}

func (w *cloudnodeAdminWire) BeginCollectorPublishOperation(ctx context.Context, req *adminpb.BeginCollectorPublishOperationReq) (*adminpb.CollectorPublishOperationRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "BeginCollectorPublishOperation", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishOperationRsp{RetInfo: &adminpb.RetInfo{}, Active: true}, nil
}

func (w *cloudnodeAdminWire) RenewCollectorPublishOperation(ctx context.Context, req *adminpb.CollectorPublishOperationReq) (*adminpb.CollectorPublishOperationRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishOperation", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishOperationRsp{RetInfo: &adminpb.RetInfo{}, Active: true}, nil
}

func (w *cloudnodeAdminWire) EndCollectorPublishOperation(ctx context.Context, req *adminpb.CollectorPublishOperationReq) (*adminpb.CollectorPublishOperationRsp, error) {
	if err := w.verify(ctx, "trpc.moox.admin.CollectorPublishLease", "EndCollectorPublishOperation", req); err != nil {
		return nil, err
	}
	return &adminpb.CollectorPublishOperationRsp{RetInfo: &adminpb.RetInfo{}, Active: true}, nil
}
