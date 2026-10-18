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

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

const (
	testSkillGatewaySecret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSkillPrimarySecret = "storage-primary-secret"
)

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
