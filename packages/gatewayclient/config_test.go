package gatewayclient

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/codec"
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

func TestExternalFileConfigRealTRPCUsesAssignedCredentialAndFixedInstance(t *testing.T) {
	// Neither a local gateway nor environment credentials are needed by an
	// externally packaged client. The explicit key file is its only identity.
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "unrelated-key")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "unrelated-signing-key-at-least-32-bytes")
	t.Setenv("MOOX_GATEWAY_CALLER", "admin")
	t.Setenv("MOOX_ACCESS_ADDRESS", "192.0.2.99:1")
	t.Setenv("MOOX_ACCESS_ID", "access@unrelated")
	for _, principal := range []struct{ caller, service, method string }{
		{"scf-collector", "trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch"},
		{"factor-engine", "trpc.moox.factor.FactorEngine", "SyncEngineCatalog"},
		{"moox-skill", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows"},
	} {
		t.Run(principal.caller, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config", "app.yaml")
			keyPath := filepath.Join(root, "secrets", "external.key")
			require.NoError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
			secret := "external-fixture-signing-key-at-least-32-bytes"
			require.NoError(t, os.WriteFile(keyPath, []byte(secret+"\n"), 0o600))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			counted := &countingListener{Listener: listener}
			credential := gatewayauth.Credentials{Caller: principal.caller, KeyID: "assigned-external-key-83", Secret: secret}
			metadata := CallMetadata{SpaceID: "crypto", TraceID: "external-config-fixture"}
			startWireServer(t, counted, credential, "access@storage", metadata)
			var config ExternalFileConfig
			decoder := yaml.NewDecoder(strings.NewReader("access_address: " + listener.Addr().String() + "\naccess_id: access@storage\ncaller: " + principal.caller + "\nkey_id: " + credential.KeyID + "\nkey_file: ../secrets/external.key\n"))
			decoder.KnownFields(true)
			require.NoError(t, decoder.Decode(&config))
			client, err := config.OpenExternal(configPath)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			require.Equal(t, credential, client.config.Credentials)
			require.Empty(t, client.Directory().Version)
			require.Nil(t, client.config.Source)
			require.Empty(t, client.config.CachePath)
			require.Empty(t, client.config.CAFile)
			ctx, cancel := context.WithTimeout(WithCallMetadata(context.Background(), metadata), 3*time.Second)
			defer cancel()
			for _, request := range []struct {
				serialization int
				body          []byte
			}{
				{codec.SerializationTypePB, []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}},
				{codec.SerializationTypeJSON, []byte(" {\n\"value\": 1e+03 }\n")},
			} {
				response, err := client.Forward(ctx, principal.service, principal.method, request.serialization, request.body)
				require.NoError(t, err)
				require.Equal(t, request.body, response, "forwarding must retain signed bytes")
			}
			var response wrapperspb.StringValue
			require.NoError(t, client.Invoke(ctx, principal.service, principal.method, wrapperspb.String("native object"), &response))
			require.Equal(t, "native object", response.Value)
			require.EqualValues(t, 1, counted.accepted.Load(), "the external client owns and reuses one connection")
			_, err = client.Forward(ctx, secretService, "GetSecret", codec.SerializationTypePB, nil)
			require.ErrorContains(t, err, "not allowed")
			require.EqualValues(t, 1, counted.accepted.Load())
			require.NoError(t, client.Close())
			client.pool.mu.Lock()
			remaining := len(client.pool.conns)
			client.pool.mu.Unlock()
			require.Zero(t, remaining)
			_, err = client.Forward(ctx, principal.service, principal.method, codec.SerializationTypePB, nil)
			require.ErrorIs(t, err, net.ErrClosed)
		})
	}
}

func TestExternalFileConfigRejectsPublicConfigurationBeforeReadingKeys(t *testing.T) {
	valid := ExternalFileConfig{Address: "access.example.test:11004", InstanceID: "access@storage",
		Caller: "moox-skill", KeyID: "assigned-skill-45", KeyFile: "../secrets/skill.key"}
	for _, change := range []struct {
		name string
		edit func(*ExternalFileConfig)
	}{
		{"url", func(c *ExternalFileConfig) { c.Address = "ip://access.example.test:11004" }},
		{"missing-port", func(c *ExternalFileConfig) { c.Address = "access.example.test" }},
		{"invalid-port", func(c *ExternalFileConfig) { c.Address = "access.example.test:65536" }},
		{"bare-host-id", func(c *ExternalFileConfig) { c.InstanceID = "storage" }},
		{"unknown-host-id", func(c *ExternalFileConfig) { c.InstanceID = "access@Storage" }},
		{"internal-caller", func(c *ExternalFileConfig) { c.Caller = "collector" }},
		{"unknown-caller", func(c *ExternalFileConfig) { c.Caller = "other" }},
		{"empty-key-id", func(c *ExternalFileConfig) { c.KeyID = "" }},
		{"key-id-path", func(c *ExternalFileConfig) { c.KeyID = "../key" }},
		{"key-id-whitespace", func(c *ExternalFileConfig) { c.KeyID = "key id" }},
		{"long-key-id", func(c *ExternalFileConfig) { c.KeyID = strings.Repeat("x", 129) }},
		{"empty-key-file", func(c *ExternalFileConfig) { c.KeyFile = "" }},
		{"key-file-whitespace", func(c *ExternalFileConfig) { c.KeyFile = " key" }},
		{"key-file-control", func(c *ExternalFileConfig) { c.KeyFile = "key\x00" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			config := valid
			change.edit(&config)
			require.Error(t, config.Validate())
			_, err := config.OpenExternal("config/app.yaml")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "load gateway signing key", "validate before opening private files")
		})
	}
	require.NoError(t, valid.Validate(), "validation must not require an installed key file")
	_, err := valid.OpenExternal("")
	require.ErrorContains(t, err, "requires a configuration path")
}

func TestExternalFileConfigPrivateKeyCannotFallBackToEnvironment(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "environment-signing-key-at-least-32-bytes")
	root := t.TempDir()
	config := ExternalFileConfig{Address: "access.example.test:11004", InstanceID: "access@storage",
		Caller: "factor-engine", KeyID: "assigned-factor-key-19", KeyFile: filepath.Join(root, "missing.key")}
	_, err := config.OpenExternal(filepath.Join(root, "app.yaml"))
	require.ErrorContains(t, err, "load gateway signing key")
	keyPath := filepath.Join(root, "invalid.key")
	require.NoError(t, os.WriteFile(keyPath, []byte("short-key"), 0o600))
	config.KeyFile = keyPath
	_, err = config.OpenExternal(filepath.Join(root, "app.yaml"))
	require.ErrorContains(t, err, "at least 32 bytes")
	secret := "valid-signing-key-at-least-32-bytes"
	require.NoError(t, os.WriteFile(keyPath, []byte(secret), 0o600))
	config.KeyFile = filepath.Join(root, "symlink.key")
	require.NoError(t, os.Symlink(keyPath, config.KeyFile))
	_, err = config.OpenExternal(filepath.Join(root, "app.yaml"))
	require.ErrorContains(t, err, "not a symlink")
	require.NotContains(t, err.Error(), secret)
	config.KeyFile = keyPath
	client, err := config.OpenExternal(filepath.Join(root, "app.yaml"))
	require.NoError(t, err, "an absolute, private key path is supported")
	require.NoError(t, client.Close())
}
