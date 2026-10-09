package gatewayclient

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestFileConfigUsesDeploymentIdentityAndCachedDirectory(t *testing.T) {
	root := t.TempDir()
	moduleConfig := filepath.Join(root, "admin", "config", "app.yaml")
	hostConfig := filepath.Join(root, "hostgateway", "config", "app.yaml")
	keyFile := filepath.Join(root, "secrets", "caller-admin.key")
	for _, path := range []string{moduleConfig, hostConfig, keyFile} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	}
	require.NoError(t, os.WriteFile(keyFile, []byte("admin-signing-fixture-key-at-least-32-bytes\n"), 0o600))
	ca, _, _ := testCertificates(t)
	host := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "host-signing-key")
	host.TLS.CAFile = ca
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host.Server.LocalAddr = listener.Addr().String()
	encoded, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostConfig, encoded, 0o600))
	directory := testDirectory(t, "storage")
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	directorypb.RegisterDirectoryService(svc, directoryServiceFunc(func(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
		if req.GetCurrentVersion() == directory.Version {
			return &directorypb.GetDirectoryRsp{Version: directory.Version}, nil
		}
		return &directorypb.GetDirectoryRsp{Changed: true, Version: directory.Version,
			Services: map[string]*directorypb.ServiceHosts{secretService: {HostIds: []string{"storage"}}},
			Hosts:    map[string]*directorypb.DirectoryHost{"storage": {Address: "storage.example.test"}}}, nil
	}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })

	// Legacy environment values must not select a host or supply a signing key.
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "ip://192.0.2.99:11001")
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", "wrong-host")
	config := FileConfig{Caller: "admin", KeyID: "independent-admin-key-22", KeyFile: "../../secrets/caller-admin.key"}
	data := filepath.Join(root, "data", "admin")
	client, err := config.OpenInternal(moduleConfig, data, nil)
	require.NoError(t, err)
	require.Equal(t, "admin", client.config.Credentials.Caller)
	require.Equal(t, config.KeyID, client.config.Credentials.KeyID)
	require.Equal(t, "storage", client.config.LocalHostID)
	require.Equal(t, listener.Addr().String(), client.config.LocalAddress)
	require.Equal(t, ca, client.config.CAFile)
	require.Equal(t, directory, client.Directory())
	require.Equal(t, filepath.Join(data, "gatewayclient", "directory.json"), client.config.CachePath)
	require.NoError(t, client.Close())
	require.NoError(t, svc.Close(nil))

	// A restart can use a previously verified directory while discovery is down.
	start := time.Now()
	client, err = config.OpenInternal(moduleConfig, data, nil)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 7*time.Second)
	require.Equal(t, directory, client.Directory())
	require.NoError(t, client.Close())

	config.KeyID = ""
	_, err = config.OpenInternal(moduleConfig, data, nil)
	require.ErrorContains(t, err, "requires key_id")
}

func TestFileConfigRejectsInvalidIdentityAndPaths(t *testing.T) {
	for _, config := range []FileConfig{
		{Caller: "unknown", KeyFile: "key"},
		{Caller: "host-gateway", KeyFile: "key"},
		{Caller: "admin", KeyID: "bad key", KeyFile: "key"},
		{Caller: "admin", KeyID: "../key", KeyFile: "key"},
		{Caller: "admin", KeyFile: " key"},
		{Caller: "admin", KeyFile: ""},
	} {
		require.Error(t, config.Validate())
	}
}
