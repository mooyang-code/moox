package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	testSkillGatewaySecret = "gateway-skill-secret"
	testSkillPrimarySecret = "storage-primary-secret"
)

func TestSetupExportSkillConfigWritesStrict0600ConfigWithoutLeakingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	output := filepath.Join(t.TempDir(), "data-access.yaml")
	want := testSkillDataAccessConfig()
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			return want, nil
		},
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"export-skill-config", "--file", "custom.toml", "--space", "crypto_market", "--output", output})
	require.NoError(t, cmd.Execute())

	info, err := os.Lstat(output)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(output)
	require.NoError(t, err)
	var got dataAccessConfig
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	require.NoError(t, decoder.Decode(&got))
	require.Equal(t, want, got)
	require.JSONEq(t, `{"status":"exported","output":"`+output+`"}`, stdout.String())
	combined := stdout.String() + stderr.String()
	require.NotContains(t, combined, testSkillGatewaySecret)
	require.NotContains(t, combined, testSkillPrimarySecret)
	require.NotContains(t, combined, want.Storage.AppKey)
}

func TestSetupExportSkillConfigRejectsUnsafeOutput(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	require.NoError(t, os.WriteFile(target, []byte("preserve"), 0o600))
	link := filepath.Join(dir, "data-access.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			return testSkillDataAccessConfig(), nil
		},
	})
	cmd.SetArgs([]string{"export-skill-config", "--space", "crypto_market", "--output", link})
	err := cmd.Execute()
	require.ErrorContains(t, err, "symlink")
	raw, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Equal(t, "preserve", string(raw))
}

func TestSetupExportSkillConfigVerifiesSnapshotBeforeWriting(t *testing.T) {
	snapshot, path := setupSkillSnapshotWithPath(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	output := filepath.Join(t.TempDir(), "data-access.yaml")
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			require.NoError(t, os.WriteFile(path, []byte("[admin]\nusername='changed'\n"), 0o600))
			return testSkillDataAccessConfig(), nil
		},
	})
	cmd.SetArgs([]string{"export-skill-config", "--space", "crypto_market", "--output", output})
	require.ErrorContains(t, cmd.Execute(), "config_changed")
	_, err := os.Lstat(output)
	require.True(t, os.IsNotExist(err))
}

func TestSetupExportSkillConfigRejectsSetupFileAsOutput(t *testing.T) {
	snapshot, path := setupSkillSnapshotWithPath(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	called := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			called = true
			return testSkillDataAccessConfig(), nil
		},
	})
	cmd.SetArgs([]string{"export-skill-config", "--file", path, "--space", "crypto_market", "--output", path})
	require.ErrorContains(t, cmd.Execute(), "must be different files")
	require.False(t, called)
}

func TestBuildSkillDataAccessConfigSelectsExactSpaceAndValidatesMaterial(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market_archive", "ip://wrong:11003", "control")
	snapshot.Manifest.SCFFetcher.Spaces = append(snapshot.Manifest.SCFFetcher.Spaces, setupconfig.SCFFetcherSpace{
		SpaceID: "crypto_market", StorageRPCGatewayTarget: "ip://203.0.113.8:11003", StorageGatewayNodeID: "control",
	})
	paths := snapshot.Manifest.Paths.Resolved()
	read := func(_ context.Context, host setupconfig.Host, path string) ([]byte, error) {
		switch path {
		case filepath.Join(paths.ControlRoot, "secrets/gateway-moox-skill.key"):
			require.Equal(t, "control", host.Name)
			return []byte(testSkillGatewaySecret + "\n"), nil
		case filepath.Join(paths.ControlRoot, "secrets/gateway-service.env"):
			require.Equal(t, "control", host.Name)
			return []byte("control"), nil
		case filepath.Join(paths.ControlRoot, "secrets/gateway-credentials.json"):
			require.Equal(t, "control", host.Name)
			return testSkillRegistryRaw(), nil
		case filepath.Join(paths.StorageRoot, "secrets/storage-internal-auth.env"):
			require.Equal(t, "control", host.Name)
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		default:
			return nil, errors.New("unexpected path")
		}
	}
	got, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "ip://203.0.113.8:11003", got.Gateway.Target)
	require.Equal(t, "control", got.Gateway.TargetNode)
	require.Equal(t, testSkillGatewaySecret, got.Gateway.Secret)
	require.Equal(t, security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill")), got.Storage.AppKey)
	require.Equal(t, "binance_spot_kline_1m", got.DataTypes["crypto"].Exchanges["binance"].KlineDatasets["1m"])
	require.Equal(t, "stock_cn_kline", got.DataTypes["stock_cn"].Exchanges["stock_cn"].KlineDatasets["1m"])
	require.Empty(t, got.DataTypes["stock_cn"].Exchanges["stock_cn"].SeriesTag)

	_, err = buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "CRYPTO_MARKET", read)
	require.ErrorContains(t, err, "space")
}

func TestBuildSkillDataAccessConfigRejectsMissingTargetNodeAndRemoteSecrets(t *testing.T) {
	tests := []struct {
		name   string
		target string
		node   string
		read   skillSecretReader
		want   string
	}{
		{name: "target", node: "control", read: func(context.Context, setupconfig.Host, string) ([]byte, error) { return nil, nil }, want: "storage_rpc_gateway_target"},
		{name: "node", target: "ip://203.0.113.8:11003", read: func(context.Context, setupconfig.Host, string) ([]byte, error) { return nil, nil }, want: "storage_gateway_node_id"},
		{name: "gateway key", target: "ip://203.0.113.8:11003", node: "control", read: func(context.Context, setupconfig.Host, string) ([]byte, error) { return nil, errors.New("missing") }, want: "Gateway snapshot"},
		{name: "gateway identity", target: "ip://203.0.113.8:11003", node: "control", read: func(_ context.Context, _ setupconfig.Host, path string) ([]byte, error) {
			if filepath.Base(path) == "gateway-moox-skill.key" {
				return []byte(testSkillGatewaySecret), nil
			}
			return nil, errors.New("missing")
		}, want: "Gateway snapshot"},
		{name: "root", target: "ip://203.0.113.8:11003", node: "control", read: func(_ context.Context, _ setupconfig.Host, path string) ([]byte, error) {
			if filepath.Base(path) == "gateway-moox-skill.key" {
				return []byte(testSkillGatewaySecret), nil
			}
			if filepath.Base(path) == "gateway-service.env" {
				return []byte("control"), nil
			}
			if filepath.Base(path) == "gateway-credentials.json" {
				return testSkillRegistryRaw(), nil
			}
			return []byte("MOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		}, want: "Storage auth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := setupSkillSnapshot(t, "crypto_market", test.target, test.node)
			_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", test.read)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestBuildSkillDataAccessConfigReadsGatewayIdentityFromTargetHost(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://198.51.100.9:11003", "storage-node")
	snapshot.Manifest.ControlHost.Name = "Primary-Control"
	snapshot.Manifest.OtherHosts = []setupconfig.Host{{Name: "Storage-Node", Address: "198.51.100.9", Port: 22, Username: "ubuntu"}}
	paths := snapshot.Manifest.Paths.Resolved()
	const deployedStoragePrimarySecret = "storage-host-primary-secret"
	const staleControlPrimarySecret = "stale-control-primary-secret"
	controlAuthRead := false
	read := func(_ context.Context, host setupconfig.Host, path string) ([]byte, error) {
		switch filepath.Base(path) {
		case "gateway-moox-skill.key":
			require.Equal(t, "Storage-Node", host.Name)
			require.Equal(t, filepath.Join(paths.StorageRoot, "secrets/gateway-moox-skill.key"), path)
			return []byte(testSkillGatewaySecret), nil
		case "gateway-service.env":
			require.Equal(t, "Storage-Node", host.Name)
			require.Equal(t, filepath.Join(paths.StorageRoot, "secrets/gateway-service.env"), path)
			return []byte("Storage-Node"), nil
		case "gateway-credentials.json":
			require.Equal(t, "Storage-Node", host.Name)
			require.Equal(t, filepath.Join(paths.StorageRoot, "secrets/gateway-credentials.json"), path)
			return testSkillRegistryRaw(), nil
		case "storage-internal-auth.env":
			if host.Name == "Primary-Control" {
				controlAuthRead = true
				return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + staleControlPrimarySecret + "\n"), nil
			}
			require.Equal(t, "Storage-Node", host.Name)
			require.Equal(t, filepath.Join(paths.StorageRoot, "secrets/storage-internal-auth.env"), path)
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + deployedStoragePrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		default:
			return nil, errors.New("unexpected path")
		}
	}
	got, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "Storage-Node", got.Gateway.TargetNode)
	require.Equal(t, security.HMACSHA256Hex(deployedStoragePrimarySecret, []byte("moox-skill")), got.Storage.AppKey)
	require.NotEqual(t, security.HMACSHA256Hex(staleControlPrimarySecret, []byte("moox-skill")), got.Storage.AppKey)
	require.False(t, controlAuthRead)
}

func TestBuildSkillDataAccessConfigRejectsUnknownStorageRootBeforeReadingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	snapshot.Manifest.Paths.StorageRoot = "relative/storage"
	readCalled := false
	_, err := buildSkillDataAccessConfig(context.Background(), snapshot, "crypto_market",
		func(context.Context, setupconfig.Host, string) (skillGatewaySnapshot, error) {
			readCalled = true
			return skillGatewaySnapshot{}, errors.New("must not read")
		},
		func(context.Context, setupconfig.Host, string) ([]byte, error) {
			readCalled = true
			return nil, errors.New("must not read")
		})
	require.ErrorContains(t, err, "Storage deployment placement")
	require.False(t, readCalled)
}

func TestBuildSkillDataAccessConfigCanonicalizesControlGatewayNode(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "PRIMARY-CONTROL")
	snapshot.Manifest.ControlHost.Name = "Primary-Control"
	paths := snapshot.Manifest.Paths.Resolved()
	read := func(_ context.Context, host setupconfig.Host, path string) ([]byte, error) {
		require.Equal(t, "Primary-Control", host.Name)
		switch filepath.Base(path) {
		case "gateway-moox-skill.key":
			require.Contains(t, path, paths.ControlRoot)
			return []byte(testSkillGatewaySecret), nil
		case "gateway-service.env":
			require.Contains(t, path, paths.ControlRoot)
			return []byte("control"), nil
		case "gateway-credentials.json":
			require.Contains(t, path, paths.ControlRoot)
			return testSkillRegistryRaw(), nil
		case "storage-internal-auth.env":
			require.Equal(t, filepath.Join(paths.StorageRoot, "secrets/storage-internal-auth.env"), path)
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		default:
			return nil, errors.New("unexpected path")
		}
	}
	got, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "control", got.Gateway.TargetNode)

	snapshot.Manifest.SCFFetcher.Spaces[0].StorageGatewayNodeID = "CONTROL"
	got, err = buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "control", got.Gateway.TargetNode)
}

func TestBuildSkillDataAccessConfigRejectsMismatchedDeployedGatewayIdentity(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	read := func(_ context.Context, _ setupconfig.Host, path string) ([]byte, error) {
		switch filepath.Base(path) {
		case "gateway-moox-skill.key":
			return []byte(testSkillGatewaySecret), nil
		case "gateway-service.env":
			return []byte("other-node"), nil
		case "gateway-credentials.json":
			return testSkillRegistryRaw(), nil
		default:
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		}
	}
	_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.ErrorContains(t, err, "identity")
}

func TestValidateSkillGatewayTargetMatchesSelectedHost(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  string
		address string
		wantErr bool
	}{
		{name: "ipv6 equivalent", target: "ip://[2001:0db8:0:0::1]:11003", address: "2001:db8::1"},
		{name: "hostname case and trailing dot", target: "ip://GW.Example.COM.:11003", address: "gw.example.com"},
		{name: "primary store port", target: "ip://198.51.100.9:20102", address: "198.51.100.9", wantErr: true},
		{name: "other gateway port", target: "ip://198.51.100.9:11004", address: "198.51.100.9", wantErr: true},
		{name: "same loopback is explicit", target: "ip://127.0.0.1:11003", address: "127.0.0.1"},
		{name: "different ip", target: "ip://198.51.100.8:11003", address: "198.51.100.9", wantErr: true},
		{name: "loopback hides public host", target: "ip://127.0.0.1:11003", address: "198.51.100.9", wantErr: true},
		{name: "unspecified hides public host", target: "ip://0.0.0.0:11003", address: "198.51.100.9", wantErr: true},
		{name: "hostname mismatch", target: "ip://other.example.com:11003", address: "gateway.example.com", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSkillGatewayTarget(test.target, setupconfig.Host{Name: "gateway", Address: test.address})
			if test.wantErr {
				require.ErrorContains(t, err, "target")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBuildSkillDataAccessConfigRejectsGatewayTargetHostMismatchBeforeReadingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://198.51.100.9:11003", "control")
	readCalled := false
	_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", func(context.Context, setupconfig.Host, string) ([]byte, error) {
		readCalled = true
		return nil, errors.New("must not read")
	})
	require.ErrorContains(t, err, "target host")
	require.False(t, readCalled)
}

func TestBuildSkillDataAccessConfigRejectsGatewayTargetPortBeforeReadingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:20102", "control")
	readCalled := false
	_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", func(context.Context, setupconfig.Host, string) ([]byte, error) {
		readCalled = true
		return nil, errors.New("must not read")
	})
	require.ErrorContains(t, err, "11003")
	require.False(t, readCalled)
}

func TestReadRemoteGatewayNodeIDDoesNotExposeServiceRootSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-service.env")
	const rootSecret = "root-service-secret-must-stay-remote"
	require.NoError(t, os.WriteFile(path, []byte("MOOX_GATEWAY_NODE_ID=Storage-Node_1\nMOOX_GATEWAY_SERVICE_KEY_ID=moox-gateway-service\nMOOX_GATEWAY_CALLER=admin-gateway\nMOOX_GATEWAY_SERVICE_SECRET_KEY="+rootSecret+"\n"), 0o600))
	transport := &recordingSkillSSH{}
	nodeID, err := readRemoteGatewayNodeID(context.Background(), transport, path)
	require.NoError(t, err)
	require.Equal(t, "Storage-Node_1", nodeID)
	require.NotContains(t, nodeID, rootSecret)
	require.NotContains(t, transport.stdout, rootSecret)
}

func TestReadRemoteSkillGatewaySnapshotUsesOneStableReadWithoutRootSecret(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	require.NoError(t, os.MkdirAll(secrets, 0o700))
	const rootSecret = "gateway-root-must-never-leave-host"
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "gateway-moox-skill.key"), []byte(testSkillGatewaySecret+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "gateway-service.env"), []byte("MOOX_GATEWAY_NODE_ID=storage\nMOOX_GATEWAY_SERVICE_SECRET_KEY="+rootSecret+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "gateway-credentials.json"), testSkillRegistryRaw(), 0o600))
	transport := &recordingSkillSSH{}

	got, err := readRemoteSkillGatewaySnapshot(context.Background(), transport, root)
	require.NoError(t, err)
	require.Equal(t, testSkillGatewaySecret+"\n", string(got.Secret))
	require.Equal(t, "storage", got.NodeID)
	require.Equal(t, testSkillRegistryRaw(), got.Registry)
	require.Equal(t, 1, transport.calls)
	require.NotContains(t, transport.stdout, rootSecret)
}

func TestBuildSkillDataAccessConfigFailsClosedOnGatewaySnapshotRotation(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://203.0.113.8:11003", "control")
	transport := &rotatingSkillSSH{}
	storageRead := false
	_, err := buildSkillDataAccessConfig(context.Background(), snapshot, "crypto_market",
		func(ctx context.Context, _ setupconfig.Host, root string) (skillGatewaySnapshot, error) {
			return readRemoteSkillGatewaySnapshot(ctx, transport, root)
		},
		func(context.Context, setupconfig.Host, string) ([]byte, error) {
			storageRead = true
			return nil, errors.New("must not read")
		})
	require.ErrorContains(t, err, "snapshot")
	require.False(t, storageRead)
	require.Equal(t, 1, transport.calls)
}

func TestReadRemoteGatewayNodeIDRejectsUnsafeOrAmbiguousFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		mode    os.FileMode
		link    bool
	}{
		{name: "missing value", content: "MOOX_GATEWAY_SERVICE_SECRET_KEY=secret\n", mode: 0o600},
		{name: "duplicate", content: "MOOX_GATEWAY_NODE_ID=one\nMOOX_GATEWAY_NODE_ID=two\n", mode: 0o600},
		{name: "dangerous", content: "MOOX_GATEWAY_NODE_ID=$(id)\n", mode: 0o600},
		{name: "carriage return", content: "MOOX_GATEWAY_NODE_ID=control\r\n", mode: 0o600},
		{name: "mode", content: "MOOX_GATEWAY_NODE_ID=control\n", mode: 0o644},
		{name: "symlink", content: "MOOX_GATEWAY_NODE_ID=control\n", mode: 0o600, link: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target.env")
			require.NoError(t, os.WriteFile(target, []byte(test.content), test.mode))
			path := target
			if test.link {
				path = filepath.Join(dir, "gateway.env")
				require.NoError(t, os.Symlink(target, path))
			}
			_, err := readRemoteGatewayNodeID(context.Background(), localSkillSSH{}, path)
			require.ErrorContains(t, err, "unavailable")
		})
	}
}

func TestBuildSkillDataAccessConfigSupportsExistingGatewayServiceEnv(t *testing.T) {
	root := t.TempDir()
	controlRoot := filepath.Join(root, "control")
	storageRoot := filepath.Join(root, "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(controlRoot, "secrets"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(storageRoot, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/gateway-moox-skill.key"), []byte(testSkillGatewaySecret), 0o600))
	oldServiceEnv := "MOOX_GATEWAY_NODE_ID=storage\nMOOX_GATEWAY_SERVICE_KEY_ID=moox-gateway-service\nMOOX_GATEWAY_CALLER=admin-gateway\nMOOX_GATEWAY_SERVICE_SECRET_KEY=existing-root-secret\n"
	require.NotContains(t, oldServiceEnv, "MOOX_GATEWAY_TARGET_NODE")
	require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/gateway-service.env"), []byte(oldServiceEnv), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/gateway-credentials.json"), testSkillRegistryRaw(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/storage-internal-auth.env"), []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET="+testSkillPrimarySecret+"\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), 0o600))

	snapshot := setupSkillSnapshot(t, "crypto_market", "ip://198.51.100.9:11003", "storage")
	snapshot.Manifest.Paths.ControlRoot = controlRoot
	snapshot.Manifest.Paths.StorageRoot = storageRoot
	snapshot.Manifest.OtherHosts = []setupconfig.Host{{Name: "storage", Address: "198.51.100.9", Port: 22, Username: "ubuntu"}}
	read := func(ctx context.Context, _ setupconfig.Host, path string) ([]byte, error) {
		if filepath.Base(path) == "gateway-service.env" {
			nodeID, err := readRemoteGatewayNodeID(ctx, localSkillSSH{}, path)
			return []byte(nodeID), err
		}
		return readRemoteSkillSecret(ctx, localSkillSSH{}, path)
	}
	config, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "storage", config.Gateway.TargetNode)
	require.Equal(t, testSkillGatewaySecret, config.Gateway.Secret)
}

func TestReadRemoteSkillSecretRejectsUnsafeRemoteFiles(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.key")
	require.NoError(t, os.WriteFile(valid, []byte("secret\n"), 0o600))
	got, err := readRemoteSkillSecret(context.Background(), localSkillSSH{}, valid)
	require.NoError(t, err)
	require.Equal(t, "secret\n", string(got))

	unsafeMode := filepath.Join(dir, "mode.key")
	require.NoError(t, os.WriteFile(unsafeMode, []byte("secret"), 0o644))
	symlink := filepath.Join(dir, "link.key")
	require.NoError(t, os.Symlink(valid, symlink))
	empty := filepath.Join(dir, "empty.key")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	oversize := filepath.Join(dir, "oversize.key")
	require.NoError(t, os.WriteFile(oversize, bytes.Repeat([]byte("x"), 4097), 0o600))
	for _, path := range []string{filepath.Join(dir, "missing.key"), unsafeMode, symlink, empty, oversize} {
		_, err := readRemoteSkillSecret(context.Background(), localSkillSSH{}, path)
		require.ErrorContains(t, err, "unavailable", path)
	}
}

func TestBuildSkillDataAccessConfigRejectsUnsafeTargetGatewayKey(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string) string
	}{
		{name: "missing", prepare: func(_ *testing.T, path string) string { return path }},
		{name: "symlink", prepare: func(t *testing.T, path string) string {
			target := path + ".target"
			require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))
			require.NoError(t, os.Symlink(target, path))
			return path
		}},
		{name: "mode", prepare: func(t *testing.T, path string) string {
			require.NoError(t, os.WriteFile(path, []byte("secret"), 0o644))
			return path
		}},
		{name: "size", prepare: func(t *testing.T, path string) string {
			require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), 4097), 0o600))
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			controlRoot := filepath.Join(root, "control")
			storageRoot := filepath.Join(root, "storage")
			require.NoError(t, os.MkdirAll(filepath.Join(controlRoot, "secrets"), 0o700))
			require.NoError(t, os.MkdirAll(filepath.Join(storageRoot, "secrets"), 0o700))
			keyPath := test.prepare(t, filepath.Join(storageRoot, "secrets/gateway-moox-skill.key"))
			require.Equal(t, filepath.Join(storageRoot, "secrets/gateway-moox-skill.key"), keyPath)

			snapshot := setupSkillSnapshot(t, "crypto_market", "ip://198.51.100.9:11003", "storage")
			snapshot.Manifest.Paths.ControlRoot = controlRoot
			snapshot.Manifest.Paths.StorageRoot = storageRoot
			snapshot.Manifest.OtherHosts = []setupconfig.Host{{Name: "storage", Address: "198.51.100.9", Port: 22, Username: "ubuntu"}}
			read := func(ctx context.Context, _ setupconfig.Host, path string) ([]byte, error) {
				return readRemoteSkillSecret(ctx, localSkillSSH{}, path)
			}
			_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
			require.ErrorContains(t, err, "Gateway snapshot")
		})
	}
}

func TestBuildSkillDataAccessConfigRequiresRegisteredSkillIdentity(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{name: "missing", prepare: func(*testing.T, string) {}, want: "Gateway snapshot"},
		{name: "symlink", prepare: func(t *testing.T, path string) {
			target := path + ".target"
			require.NoError(t, os.WriteFile(target, testSkillRegistryRaw(), 0o600))
			require.NoError(t, os.Symlink(target, path))
		}, want: "Gateway snapshot"},
		{name: "mode", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, testSkillRegistryRaw(), 0o644))
		}, want: "Gateway snapshot"},
		{name: "malformed", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1`), 0o600))
		}},
		{name: "unknown field", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"credentials":[],"extra":true}`), 0o600))
		}},
		{name: "trailing value", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, append(testSkillRegistryRaw(), []byte(` {}`)...), 0o600))
		}},
		{name: "size", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), 4097), 0o600))
		}, want: "Gateway snapshot"},
		{name: "unregistered", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"credentials":[{"key_id":"collector","caller":"collector","secret_file":"gateway-collector.key"}]}`), 0o600))
		}},
		{name: "wrong caller", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"credentials":[{"key_id":"moox-skill","caller":"other","secret_file":"gateway-moox-skill.key"}]}`), 0o600))
		}},
		{name: "wrong secret file", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"credentials":[{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-other.key"}]}`), 0o600))
		}},
		{name: "duplicate identity", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"credentials":[{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"},{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"}]}`), 0o600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			controlRoot := filepath.Join(root, "control")
			storageRoot := filepath.Join(root, "storage")
			require.NoError(t, os.MkdirAll(filepath.Join(controlRoot, "secrets"), 0o700))
			require.NoError(t, os.MkdirAll(filepath.Join(storageRoot, "secrets"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/gateway-moox-skill.key"), []byte(testSkillGatewaySecret), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(storageRoot, "secrets/gateway-service.env"), []byte("MOOX_GATEWAY_NODE_ID=storage\nMOOX_GATEWAY_SERVICE_KEY_ID=moox-gateway-service\nMOOX_GATEWAY_CALLER=admin-gateway\nMOOX_GATEWAY_SERVICE_SECRET_KEY=root-service-secret\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(controlRoot, "secrets/storage-internal-auth.env"), []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET="+testSkillPrimarySecret+"\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), 0o600))
			test.prepare(t, filepath.Join(storageRoot, "secrets/gateway-credentials.json"))

			snapshot := setupSkillSnapshot(t, "crypto_market", "ip://198.51.100.9:11003", "storage")
			snapshot.Manifest.Paths.ControlRoot = controlRoot
			snapshot.Manifest.Paths.StorageRoot = storageRoot
			snapshot.Manifest.OtherHosts = []setupconfig.Host{{Name: "storage", Address: "198.51.100.9", Port: 22, Username: "ubuntu"}}
			read := func(ctx context.Context, _ setupconfig.Host, path string) ([]byte, error) {
				if filepath.Base(path) == "gateway-service.env" {
					nodeID, err := readRemoteGatewayNodeID(ctx, localSkillSSH{}, path)
					return []byte(nodeID), err
				}
				return readRemoteSkillSecret(ctx, localSkillSSH{}, path)
			}
			_, err := buildSkillDataAccessConfigFromLegacyReader(context.Background(), snapshot, "crypto_market", read)
			want := test.want
			if want == "" {
				want = "Gateway credential registry"
			}
			require.ErrorContains(t, err, want)
		})
	}
}

func TestWriteSkillConfigAtomicFailurePreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-access.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	err := writeSkillConfigAtomic0600(path, []byte("new"), func(string, string) error {
		return errors.New("rename failed")
	})
	require.ErrorContains(t, err, "rename failed")
	raw, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "old", string(raw))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), ".data-access.yaml.tmp-*"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}

type localSkillSSH struct{}

func (localSkillSSH) Check(context.Context) error { return nil }
func (localSkillSSH) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("not implemented")
}
func (localSkillSSH) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return errors.New("not implemented")
}
func (localSkillSSH) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return setupssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}
func (localSkillSSH) Close() error { return nil }

type recordingSkillSSH struct {
	localSkillSSH
	stdout string
	calls  int
}

type rotatingSkillSSH struct {
	localSkillSSH
	calls int
}

func (s *rotatingSkillSSH) Run(context.Context, []string, io.Reader) (setupssh.Result, error) {
	s.calls++
	// A transport observing an in-place rotation returns two generations. The
	// snapshot parser must reject it rather than selecting fields from either.
	return setupssh.Result{Stdout: "MOOX_SKILL_GATEWAY_SNAPSHOT_V1\nYWJj\ncontrol\ne30=\nrotated-generation\n"}, nil
}

func (s *recordingSkillSSH) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	s.calls++
	result, err := s.localSkillSSH.Run(ctx, argv, stdin)
	s.stdout = result.Stdout
	return result, err
}

func testSkillDataAccessConfig() dataAccessConfig {
	return dataAccessConfig{
		Version: 1,
		Gateway: dataGatewayConfig{Target: "ip://203.0.113.8:11003", TargetNode: "control", KeyID: "moox-skill", Caller: "moox-skill", Secret: testSkillGatewaySecret},
		Storage: dataStorageAuthConfig{AppID: "moox-skill", AppKey: security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill"))},
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {SpaceID: "crypto_market", SeriesTag: "venue:binance", KlineDatasets: map[string]string{"1m": "binance_spot_kline_1m"}},
				},
			},
			"stock_cn": {
				DefaultExchange: "stock_cn",
				Exchanges: map[string]exchangeConfig{
					"stock_cn": {SpaceID: "stock_cn", SeriesTag: "", KlineDatasets: map[string]string{"1m": "stock_cn_kline"}},
				},
			},
		},
	}
}

func testSkillRegistryRaw() []byte {
	return []byte(`{"version":1,"credentials":[{"key_id":"moox-skill","caller":"moox-skill","secret_file":"gateway-moox-skill.key"}]}`)
}

func buildSkillDataAccessConfigFromLegacyReader(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	space string,
	read skillSecretReader,
) (dataAccessConfig, error) {
	readGateway := func(ctx context.Context, host setupconfig.Host, root string) (skillGatewaySnapshot, error) {
		secret, err := read(ctx, host, filepath.Join(root, "secrets/gateway-moox-skill.key"))
		if err != nil {
			return skillGatewaySnapshot{}, err
		}
		node, err := read(ctx, host, filepath.Join(root, "secrets/gateway-service.env"))
		if err != nil {
			return skillGatewaySnapshot{}, err
		}
		registry, err := read(ctx, host, filepath.Join(root, "secrets/gateway-credentials.json"))
		if err != nil {
			return skillGatewaySnapshot{}, err
		}
		return skillGatewaySnapshot{Secret: secret, NodeID: string(node), Registry: registry}, nil
	}
	return buildSkillDataAccessConfig(ctx, snapshot, space, readGateway, read)
}

func setupSkillSnapshot(t *testing.T, space, target, node string) *setupconfig.Snapshot {
	snapshot, _ := setupSkillSnapshotWithPath(t, space, target, node)
	return snapshot
}

func setupSkillSnapshotWithPath(t *testing.T, space, target, node string) (*setupconfig.Snapshot, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	raw := []byte("[admin]\nusername='admin'\npassword='admin-secret'\n[tencent_cloud]\nsecret_id='AKID-test'\nsecret_key='cloud-secret'\n[eventbus]\npublic_address='eventbus.example.test'\nport=4333\ntls_enabled=true\n[control_host]\nname='control'\naddress='203.0.113.8'\nport=22\nusername='ubuntu'\npassword='ssh-secret'\n")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	snapshot.Manifest.SCFFetcher.Enabled = true
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		SpaceID: space, StorageRPCGatewayTarget: target, StorageGatewayNodeID: node,
	}}
	return snapshot, path
}
