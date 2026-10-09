package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	testSkillCallerSecret  = "moox-skill-caller-secret"
	testSkillPrimarySecret = "storage-primary-secret"
)

func TestSetupExportSkillConfigWritesStrict0600ConfigWithoutLeakingSecrets(t *testing.T) {
	snapshot := setupSkillSnapshot(t)
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
	require.Equal(t, want, got)
	require.JSONEq(t, `{"status":"exported","output":"`+output+`"}`, stdout.String())
	combined := stdout.String() + stderr.String()
	require.NotContains(t, combined, testSkillCallerSecret)
	require.NotContains(t, combined, testSkillPrimarySecret)
	require.NotContains(t, combined, want.Storage.AppKey)
}

func TestSetupExportSkillConfigRejectsUnsafeOutput(t *testing.T) {
	snapshot := setupSkillSnapshot(t)
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
	snapshot, path := setupSkillSnapshotWithPath(t)
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
	snapshot, path := setupSkillSnapshotWithPath(t)
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
		Version: 1,
		Access:  dataAccessEndpoint{Address: "146.56.196.204:11004", ID: "access@storage", Caller: "moox-skill", Key: "moox-skill-1:" + testSkillCallerSecret},
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

func setupSkillSnapshot(t *testing.T) *setupconfig.Snapshot {
	snapshot, _ := setupSkillSnapshotWithPath(t)
	return snapshot
}

func setupSkillSnapshotWithPath(t *testing.T) (*setupconfig.Snapshot, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.toml")
	raw := []byte("[admin]\nusername='admin'\npassword='admin-secret'\n[tencent_cloud]\nsecret_id='AKID-test'\nsecret_key='cloud-secret'\n[eventbus]\nhost='203.0.113.8'\nport=4333\ntls_enabled=true\n[hosts.\"203.0.113.8\"]\nport=22\nusername='ubuntu'\npassword='ssh-secret'\n[control_host]\nname='control'\nhost='203.0.113.8'\n")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	snapshot.Manifest.StorageHost = setupconfig.Host{Name: "storage", Address: "146.56.196.204", Port: 22, Username: "ubuntu"}
	return snapshot, path
}

// testSkillAccess 返回固定的 access@storage 公网外部接入。
func testSkillAccess(context.Context) (servicecatalog.AccessEndpoint, error) {
	return servicecatalog.AccessEndpoint{Address: "146.56.196.204:11004", ID: "access@storage", HostID: "storage"}, nil
}

// testSkillSecrets 按路径返回 control 上的 moox-skill 密钥和 storage 上的 Storage 鉴权文件。
func testSkillSecrets(t *testing.T, snapshot *setupconfig.Snapshot, callerKey []byte) skillSecretReader {
	t.Helper()
	paths := snapshot.Manifest.Paths.Resolved()
	return func(_ context.Context, host setupconfig.Host, path string) ([]byte, error) {
		switch path {
		case filepath.Join(paths.ControlRoot, "secrets/principal-moox-skill.key"):
			require.Equal(t, "control", host.Name, "moox-skill 的密钥只从 control 主机读取")
			if callerKey == nil {
				return nil, errors.New("missing")
			}
			return callerKey, nil
		case filepath.Join(paths.StorageRoot, "secrets/storage-internal-auth.env"):
			require.Equal(t, "storage", host.Name, "Storage 鉴权只从 Storage 主机读取")
			return []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + testSkillPrimarySecret + "\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	}
}

func testSkillCallerKey(t *testing.T, caller string) []byte {
	t.Helper()
	raw, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: caller, KeyID: caller + "-1", Secret: testSkillCallerSecret})
	require.NoError(t, err)
	return raw
}

func TestBuildSkillDataAccessConfigUsesAccessAndPrincipalKey(t *testing.T) {
	snapshot := setupSkillSnapshot(t)
	got, err := buildSkillDataAccessConfig(context.Background(), snapshot, " Crypto ", testSkillAccess, testSkillSecrets(t, snapshot, testSkillCallerKey(t, "moox-skill")), mustRepoSkillKlineDatasets())
	require.NoError(t, err)
	require.Equal(t, dataAccessEndpoint{Address: "146.56.196.204:11004", ID: "access@storage", Caller: "moox-skill", Key: "moox-skill-1:" + testSkillCallerSecret}, got.Access)
	require.Equal(t, security.HMACSHA256Hex(testSkillPrimarySecret, []byte("moox-skill")), got.Storage.AppKey)
	require.Equal(t, "venue:binance|market:spot|source:spot_http", got.DataTypes["crypto"].Exchanges["binance"].SeriesTag)
	require.Equal(t, map[string]string{"1m": "dataset_dasftksvjhj2jom4vhd0", "1h": "dataset_dasftksvjhj2jom4vhe0"}, got.DataTypes["crypto"].Exchanges["binance"].KlineDatasets)
	require.Equal(t, map[string]string{"1m": "dataset_dasftksvjhj2jom4vhf0"}, got.DataTypes["stockcn"].Exchanges["stockcn"].KlineDatasets)
	require.NoError(t, got.validate())
}

func TestBuildSkillDataAccessConfigFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		space     string
		callerKey func(*testing.T) []byte
		prepare   func(*setupconfig.Snapshot)
		access    skillAccessEndpoint
		want      string
	}{
		{name: "unsupported space", space: "stockus", want: "unsupported space"},
		{name: "missing key", callerKey: func(*testing.T) []byte { return nil }, want: "签名密钥"},
		{name: "other caller key", callerKey: func(t *testing.T) []byte { return testSkillCallerKey(t, "factor-engine") }, want: "factor-engine"},
		{name: "relative storage root", prepare: func(snapshot *setupconfig.Snapshot) { snapshot.Manifest.Paths.StorageRoot = "relative/storage" }, want: "安装目录"},
		{name: "no storage host", prepare: func(snapshot *setupconfig.Snapshot) { snapshot.Manifest.StorageHost = setupconfig.Host{} }, want: "Storage 主机"},
		{name: "directory unavailable", access: func(context.Context) (servicecatalog.AccessEndpoint, error) {
			return servicecatalog.AccessEndpoint{}, errors.New("控制面不可达")
		}, want: "控制面不可达"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := setupSkillSnapshot(t)
			if tc.prepare != nil {
				tc.prepare(snapshot)
			}
			space := tc.space
			if space == "" {
				space = "crypto"
			}
			key := testSkillCallerKey(t, "moox-skill")
			if tc.callerKey != nil {
				key = tc.callerKey(t)
			}
			access := tc.access
			if access == nil {
				access = testSkillAccess
			}
			_, err := buildSkillDataAccessConfig(context.Background(), snapshot, space, access, testSkillSecrets(t, snapshot, key), mustRepoSkillKlineDatasets())
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), testSkillCallerSecret)
		})
	}
}

func mustRepoSkillKlineDatasets() skillKlineDatasets {
	resolver, err := loadSkillKlineDatasets(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	if err != nil {
		panic(err)
	}
	return resolver
}
