package command

import (
	"bytes"
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	testSkillGatewaySecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSkillPrimarySecret = "storage-primary-secret"
)

func TestSetupExportSkillConfigWritesStrict0600ConfigWithoutLeakingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto", "ip://203.0.113.8:11003", "control")
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
	cmd.SetArgs([]string{"export-skill-config", "--file", "moox.toml", "--space", "crypto", "--output", output})
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
	want.SigningKey = ""
	require.Equal(t, want, got)
	require.JSONEq(t, `{"status":"exported","output":"`+output+`"}`, stdout.String())
	combined := stdout.String() + stderr.String()
	require.NotContains(t, combined, testSkillGatewaySecret)
	require.NotContains(t, combined, testSkillPrimarySecret)
	require.NotContains(t, combined, want.Storage.AppKey)
}

func TestSetupExportSkillConfigRejectsUnsafeOutput(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto", "ip://203.0.113.8:11003", "control")
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
	cmd.SetArgs([]string{"export-skill-config", "--space", "crypto", "--output", link})
	err := cmd.Execute()
	require.ErrorContains(t, err, "symlink")
	raw, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	require.Equal(t, "preserve", string(raw))
}

func TestSetupExportSkillConfigVerifiesSnapshotBeforeWriting(t *testing.T) {
	snapshot, path := setupSkillSnapshotWithPath(t, "crypto", "ip://203.0.113.8:11003", "control")
	output := filepath.Join(t.TempDir(), "data-access.yaml")
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			require.NoError(t, os.WriteFile(path, []byte("[admin]\nusername='changed'\n"), 0o600))
			return testSkillDataAccessConfig(), nil
		},
	})
	cmd.SetArgs([]string{"export-skill-config", "--space", "crypto", "--output", output})
	require.ErrorContains(t, cmd.Execute(), "config_changed")
	_, err := os.Lstat(output)
	require.True(t, os.IsNotExist(err))
}

func TestSetupExportSkillConfigRejectsSetupFileAsOutput(t *testing.T) {
	snapshot, path := setupSkillSnapshotWithPath(t, "crypto", "ip://203.0.113.8:11003", "control")
	called := false
	cmd := newSetupCommand(setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error) {
			called = true
			return testSkillDataAccessConfig(), nil
		},
	})
	cmd.SetArgs([]string{"export-skill-config", "--file", path, "--space", "crypto", "--output", path})
	require.ErrorContains(t, cmd.Execute(), "must be different files")
	require.False(t, called)
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
func (localSkillSSH) Download(context.Context, string, io.Writer) (int64, error) {
	return 0, errors.New("not implemented")
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

func testSkillDataAccessConfig() dataAccessConfig {
	return dataAccessConfig{
		GatewayClient: &gatewayclient.ExternalFileConfig{Address: "storage.example:11004", InstanceID: "access@storage", Caller: "moox-skill", KeyID: "assigned-skill-key-17", KeyFile: ".moox-skill-keys/fixture.key"}, SigningKey: testSkillGatewaySecret,
		Version: 1,
		Storage: dataStorageAuthConfig{AppID: "moox-skill", AppKey: security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill"))},
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {SpaceID: "crypto", SeriesTag: "venue:binance|market:spot|source:spot_http", KlineDatasets: map[string]string{"1m": "dataset_binance_kline_1m"}},
				},
			},
			"stockcn": {
				DefaultExchange: "stockcn",
				Exchanges: map[string]exchangeConfig{
					"stockcn": {SpaceID: "stockcn", SeriesTag: "default", KlineDatasets: map[string]string{"1m": "dataset_stockcn_equity_kline_1m"}},
				},
			},
		},
	}
}

func setupSkillSnapshot(t *testing.T, space, target, node string) *setupconfig.Snapshot {
	snapshot, _ := setupSkillSnapshotWithPath(t, space, target, node)
	return snapshot
}

func setupSkillSnapshotWithPath(t *testing.T, space, target, node string) (*setupconfig.Snapshot, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.toml")
	raw := []byte("[admin]\nusername='admin'\npassword='admin-secret'\n\n[tencent_cloud]\nsecret_id='AKID-test'\nsecret_key='cloud-secret'\n\n[eventbus]\nport=4333\ntls_enabled=true\n\n[hosts.control]\naddress = \"203.0.113.8\"\n[hosts.control.ssh]\nport=22\nusername='ubuntu'\npassword='ssh-secret'\n\n[placements]\ncontrol = [\"admin\", \"console-proxy\", \"web-host\", \"eventbus\"]\n")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	snapshot.Manifest.SCFFetcher.Enabled = true
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		SpaceID: space, AccessAddress: target, AccessID: node,
	}}
	return snapshot, path
}

// repoSkillKlineDatasets resolves Skill kline Datasets from the checked-in
// collection task seed.
func repoSkillKlineDatasets(t *testing.T) skillKlineDatasets {
	t.Helper()
	return mustRepoSkillKlineDatasets()
}

func mustRepoSkillKlineDatasets() skillKlineDatasets {
	resolver, err := loadSkillKlineDatasets(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	if err != nil {
		panic(err)
	}
	return resolver
}

func TestBuildSkillDataAccessConfigUsesStoragePlacementWithoutGatewaySecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto", "obsolete-not-used", "obsolete-not-used")
	testfixture.SetHost(&snapshot.Manifest, setupconfig.Host{Name: "storage", Address: "192.0.2.10"}, "storage-primary")
	reads := 0
	cfg, err := buildSkillDataAccessConfig(t.Context(), snapshot, "crypto", func(_ context.Context, host setupconfig.Host, path string) ([]byte, error) {
		reads++
		require.Equal(t, "storage", host.Name)
		require.Equal(t, filepath.Join(snapshot.Manifest.Paths.Resolved().StorageRoot, "secrets/storage-internal-auth.env"), path)
		return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
	}, repoSkillKlineDatasets(t))
	require.NoError(t, err)
	require.Equal(t, 1, reads)
	require.Equal(t, security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill")), cfg.Storage.AppKey)
	require.NoError(t, cfg.validate())
	raw, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "gateway")
	require.NotContains(t, string(raw), testSkillPrimarySecret)
	selection, err := cfg.resolveKline("crypto", "binance", "1m")
	require.NoError(t, err)
	require.NotEmpty(t, selection.DatasetID)
}

func TestBuildSkillDataAccessConfigRejectsMissingInputsBeforeReadingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t, "crypto", "", "")
	read := func(context.Context, setupconfig.Host, string) ([]byte, error) {
		t.Fatal("unexpected secret read")
		return nil, nil
	}
	_, err := buildSkillDataAccessConfig(t.Context(), snapshot, "missing-space", read, repoSkillKlineDatasets(t))
	require.ErrorContains(t, err, "unsupported space")
	_, err = buildSkillDataAccessConfig(t.Context(), nil, "crypto", read, repoSkillKlineDatasets(t))
	require.ErrorContains(t, err, "dependencies")
	_, err = buildSkillDataAccessConfig(t.Context(), snapshot, "crypto", func(context.Context, setupconfig.Host, string) ([]byte, error) { return nil, errors.New("unavailable") }, repoSkillKlineDatasets(t))
	require.ErrorContains(t, err, "Storage auth unavailable")
	_, err = buildSkillDataAccessConfig(t.Context(), snapshot, "crypto", func(context.Context, setupconfig.Host, string) ([]byte, error) { return []byte("invalid auth"), nil }, repoSkillKlineDatasets(t))
	require.ErrorContains(t, err, "Storage auth invalid")
}
