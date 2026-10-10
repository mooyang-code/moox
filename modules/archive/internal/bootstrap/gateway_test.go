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
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type archiveDirectoryFixture struct {
	directory servicecatalog.Directory
}

func (f archiveDirectoryFixture) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	if req.GetCurrentVersion() == f.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: f.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Changed: true, Version: f.directory.Version,
		Services: map[string]*directorypb.ServiceHosts{"trpc.moox.storage.Metadata": {HostIds: []string{"storage"}}},
		Hosts:    map[string]*directorypb.DirectoryHost{"storage": {Address: "storage.example.test"}}}, nil
}

type archiveMetadataFixture struct {
	storagepb.MetadataService
	registrations atomic.Int32
}

func (f *archiveMetadataFixture) RegisterArchiveFile(ctx context.Context, req *storagepb.RegisterArchiveFileReq) (*storagepb.RegisterArchiveFileRsp, error) {
	metadata := codec.Message(ctx).ServerMetaData()
	values := map[string]string{}
	for key, value := range metadata {
		values[strings.ToLower(key)] = string(value)
	}
	if values["x-moox-caller"] != "archive" || values["x-moox-key-id"] != "archive-generated-key-id" || values["x-moox-target-node"] != "storage" || values["x-moox-signature"] == "" {
		return nil, errors.New("archive gateway signing identity was not forwarded")
	}
	if req.GetArchiveFile().GetAttributes()["generation"] == "" {
		return nil, errors.New("archive generation is missing")
	}
	f.registrations.Add(1)
	return &storagepb.RegisterArchiveFileRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func installArchiveGatewayFixture(t *testing.T, cfg *config.Config, dir string) {
	t.Helper()
	root := filepath.Join(dir, "deployment")
	configPath := filepath.Join(root, "archive", "config", "app.yaml")
	hostPath := filepath.Join(root, "host-gateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-archive.key")
	caPath := filepath.Join(root, "certs", "moox-ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	require.NoError(t, os.WriteFile(keyPath, []byte("archive-signing-fixture-secret-at-least-32-bytes\n"), 0o600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	directory := servicecatalog.Directory{
		Services: map[string][]string{"trpc.moox.storage.Metadata": {"storage"}},
		Hosts:    map[string]servicecatalog.DirectoryHost{"storage": {Address: "storage.example.test"}},
	}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	directorypb.RegisterDirectoryService(svc, archiveDirectoryFixture{directory})
	metadata := &archiveMetadataFixture{}
	storagepb.RegisterMetadataService(svc, metadata)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	t.Cleanup(func() {
		require.Positive(t, metadata.registrations.Load(), "shutdown did not register the materialized archive file through tRPC")
	})
	host := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "host-signing-key-id")
	host.Server.LocalAddr = listener.Addr().String()
	hostRaw, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostPath, hostRaw, 0o600))
	cfg.GatewayClient.KeyID = "archive-generated-key-id"
	configRaw, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configRaw, 0o600))
	loaded, err := config.Load(configPath)
	require.NoError(t, err)
	*cfg = *loaded
}
