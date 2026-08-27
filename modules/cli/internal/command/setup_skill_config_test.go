package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
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

func TestBuildSkillDataAccessConfigSelectsExactSpaceAndValidatesMaterial(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto_market_archive", "ip://wrong:11003", "control")
	snapshot.Manifest.SCFFetcher.Spaces = append(snapshot.Manifest.SCFFetcher.Spaces, setupconfig.SCFFetcherSpace{
		SpaceID: "crypto_market", StorageRPCGatewayTarget: "ip://203.0.113.8:11003", StorageGatewayNodeID: "control",
	})
	paths := snapshot.Manifest.Paths.Resolved()
	read := func(path string) ([]byte, error) {
		switch path {
		case filepath.Join(paths.ControlRoot, "secrets/gateway-moox-skill.key"):
			return []byte(testSkillGatewaySecret + "\n"), nil
		case filepath.Join(paths.ControlRoot, "secrets/storage-internal-auth.env"):
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		default:
			return nil, errors.New("unexpected path")
		}
	}
	got, err := buildSkillDataAccessConfig(context.Background(), snapshot, "crypto_market", read)
	require.NoError(t, err)
	require.Equal(t, "ip://203.0.113.8:11003", got.Gateway.Target)
	require.Equal(t, "control", got.Gateway.TargetNode)
	require.Equal(t, testSkillGatewaySecret, got.Gateway.Secret)
	require.Equal(t, security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill")), got.Storage.AppKey)
	require.Equal(t, "binance_spot_kline_1m", got.DataTypes["crypto"].Exchanges["binance"].KlineDatasets["1m"])

	_, err = buildSkillDataAccessConfig(context.Background(), snapshot, "CRYPTO_MARKET", read)
	require.ErrorContains(t, err, "space")
}

func TestBuildSkillDataAccessConfigRejectsMissingTargetNodeAndRemoteSecrets(t *testing.T) {
	tests := []struct {
		name   string
		target string
		node   string
		read   func(string) ([]byte, error)
		want   string
	}{
		{name: "target", node: "control", read: func(string) ([]byte, error) { return nil, nil }, want: "storage_rpc_gateway_target"},
		{name: "node", target: "ip://203.0.113.8:11003", read: func(string) ([]byte, error) { return nil, nil }, want: "storage_gateway_node_id"},
		{name: "gateway key", target: "ip://203.0.113.8:11003", node: "control", read: func(string) ([]byte, error) { return nil, errors.New("missing") }, want: "Gateway credential"},
		{name: "root", target: "ip://203.0.113.8:11003", node: "control", read: func(path string) ([]byte, error) {
			if filepath.Base(path) == "gateway-moox-skill.key" {
				return []byte(testSkillGatewaySecret), nil
			}
			return []byte("MOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		}, want: "Storage auth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := setupSkillSnapshot(t, "crypto_market", test.target, test.node)
			_, err := buildSkillDataAccessConfig(context.Background(), snapshot, "crypto_market", test.read)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func testSkillDataAccessConfig() dataAccessConfig {
	return dataAccessConfig{
		Version:   1,
		Gateway:   dataGatewayConfig{Target: "ip://203.0.113.8:11003", TargetNode: "control", KeyID: "moox-skill", Caller: "moox-skill", Secret: testSkillGatewaySecret},
		Storage:   dataStorageAuthConfig{AppID: "moox-skill", AppKey: security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill"))},
		DataTypes: map[string]dataTypeConfig{"crypto": {DefaultExchange: "binance", Exchanges: map[string]exchangeConfig{"binance": {SpaceID: "crypto_market", SeriesTag: "venue:binance", KlineDatasets: map[string]string{"1m": "binance_spot_kline_1m"}}}}},
	}
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
