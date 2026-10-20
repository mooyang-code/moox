package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
)

const (
	testStorageNodeSecret    = "NODESECRETNODESECRETNODESECRET0001"
	testStoragePrimarySecret = "PRIMARYSECRETPRIMARYSECRETPRIMARY01"
	testStorageViewSecret    = "VIEWSECRETVIEWSECRETVIEWSECRETVIEW01"
)

// writeNativeStorageUnit 模拟 unitinstall 产出的原生 storage 单元：releases/<ID>/secrets 下的 0600 运行环境，
// current 为指向该发布的链接。
func writeNativeStorageUnit(t *testing.T, values map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "storage")
	secrets := filepath.Join(root, "releases", "20261018-000000", "secrets")
	require.NoError(t, os.MkdirAll(secrets, 0o700))
	raw, err := json.Marshal(values)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "runtime-storage-primary.json"), append(raw, '\n'), 0o600))
	require.NoError(t, os.Symlink(filepath.Join("releases", "20261018-000000"), filepath.Join(root, "current")))
	return root
}

func nativeStoragePrimaryEnvironment() map[string]string {
	return map[string]string{
		"MOOX_HEALTH_AUTH_VERSION":         "moox-health-v1",
		"MOOX_STORAGE_ROLE":                "primary",
		"MOOX_STORAGE_HOME":                "./var/storage",
		"MOOX_STORAGE_NODE_AUTH_SECRET":    testStorageNodeSecret,
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET": testStoragePrimarySecret,
		"MOOX_STORAGE_VIEW_AUTH_SECRET":    testStorageViewSecret,
	}
}

func TestReadRemoteStorageRoleSecretsUsesNativeUnitLayout(t *testing.T) {
	root := writeNativeStorageUnit(t, nativeStoragePrimaryEnvironment())
	secrets, err := readRemoteStorageRoleSecrets(context.Background(), localSkillSSH{}, root)
	require.NoError(t, err)
	require.Equal(t, storageRoleSecrets{Node: testStorageNodeSecret, Primary: testStoragePrimarySecret, View: testStorageViewSecret}, secrets)
	// 格式化输出不得泄露密钥。
	for _, rendered := range []string{fmt.Sprint(secrets), fmt.Sprintf("%v %+v %#v", secrets, secrets, secrets)} {
		require.NotContains(t, rendered, "SECRET")
	}
}

func TestReadRemoteStorageRoleSecretsRejectsMissingOrIncompleteEnvironment(t *testing.T) {
	// 没有原生单元（例如只有旧布局 secrets/storage-node-auth.env）时明确报不可用。
	legacy := filepath.Join(t.TempDir(), "storage")
	require.NoError(t, os.MkdirAll(filepath.Join(legacy, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "secrets", "storage-node-auth.env"), []byte("MOOX_STORAGE_NODE_AUTH_SECRET="+testStorageNodeSecret+"\n"), 0o600))
	_, err := readRemoteStorageRoleSecrets(context.Background(), localSkillSSH{}, legacy)
	require.ErrorContains(t, err, "storage_verification_auth_unavailable")

	for _, key := range []string{"MOOX_STORAGE_NODE_AUTH_SECRET", "MOOX_STORAGE_PRIMARY_AUTH_SECRET", "MOOX_STORAGE_VIEW_AUTH_SECRET"} {
		values := nativeStoragePrimaryEnvironment()
		delete(values, key)
		root := writeNativeStorageUnit(t, values)
		_, err := readRemoteStorageRoleSecrets(context.Background(), localSkillSSH{}, root)
		require.ErrorContains(t, err, "storage_role_secrets_invalid", key)
		require.NotContains(t, err.Error(), "SECRET", key)
	}

	values := nativeStoragePrimaryEnvironment()
	values["MOOX_STORAGE_VIEW_AUTH_SECRET"] = "short"
	_, err = readRemoteStorageRoleSecrets(context.Background(), localSkillSSH{}, writeNativeStorageUnit(t, values))
	require.ErrorContains(t, err, "storage_role_secrets_invalid")

	unsafe := writeNativeStorageUnit(t, nativeStoragePrimaryEnvironment())
	require.NoError(t, os.Chmod(filepath.Join(unsafe, "releases", "20261018-000000", "secrets", "runtime-storage-primary.json"), 0o644))
	_, err = readRemoteStorageRoleSecrets(context.Background(), localSkillSSH{}, unsafe)
	require.ErrorContains(t, err, "storage_verification_auth_unavailable")
}

func TestParseNativeStorageRoleSecretsRejectsMalformedJSON(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("MOOX_STORAGE_NODE_AUTH_SECRET=x\n"), []byte(`{"MOOX_STORAGE_NODE_AUTH_SECRET": 1}`)} {
		_, err := parseNativeStorageRoleSecrets(raw)
		require.ErrorContains(t, err, "storage_role_secrets_invalid")
	}
}

func TestReadRemotePrivateFileRejectsUnsafeRemoteFiles(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.key")
	require.NoError(t, os.WriteFile(valid, []byte("secret\n"), 0o600))
	got, err := readRemotePrivateFile(context.Background(), localSkillSSH{}, valid, 4096)
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
		_, err := readRemotePrivateFile(context.Background(), localSkillSSH{}, path, 4096)
		require.ErrorContains(t, err, "unavailable", path)
	}
}

func TestBuildSkillDataAccessConfigReadsNativeStoragePrimarySecret(t *testing.T) {
	snapshot := setupSnapshot(t)
	storageRoot := writeNativeStorageUnit(t, nativeStoragePrimaryEnvironment())
	snapshot.Manifest.Paths.StorageRoot = storageRoot
	var requested []string
	read := func(ctx context.Context, _ setupconfig.Host, path string) ([]byte, error) {
		requested = append(requested, path)
		return readRemotePrivateFile(ctx, localSkillSSH{}, path, nativeStorageEnvironmentLimit)
	}
	config, err := buildSkillDataAccessConfig(context.Background(), snapshot, "crypto", read, stubSkillKlineDatasets)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(storageRoot, "current", "secrets", "runtime-storage-primary.json")}, requested)
	require.Equal(t, security.HMACSHA256Hex(testStoragePrimarySecret, []byte(skillConfigIdentity)), config.Storage.AppKey)

	_, err = buildSkillDataAccessConfig(context.Background(), snapshot, "crypto", func(context.Context, setupconfig.Host, string) ([]byte, error) {
		return nil, errors.New("missing")
	}, stubSkillKlineDatasets)
	require.ErrorContains(t, err, "Storage auth unavailable")
}

func TestEnsureSetupBrowserCATrustRefreshesNativeControlCA(t *testing.T) {
	original := refreshSetupConsoleCA
	t.Cleanup(func() { refreshSetupConsoleCA = original })
	calls := 0
	refreshSetupConsoleCA = func(context.Context, setupconfig.Manifest) error {
		calls++
		return errors.New("ssh unavailable")
	}

	snapshot := setupSnapshot(t)
	// 公网地址走公共证书，不需要本机信任内部 CA。
	require.NoError(t, ensureSetupBrowserCATrust(context.Background(), snapshot))
	require.Zero(t, calls)

	control := snapshot.Manifest.ControlHost()
	control.TLSMode = "internal"
	testfixture.SetHost(&snapshot.Manifest, control)
	err := ensureSetupBrowserCATrust(context.Background(), snapshot)
	require.ErrorContains(t, err, "browser_ca_trust_failed")
	require.True(t, strings.Contains(err.Error(), "ssh unavailable"))
	require.Equal(t, 1, calls)
}

func stubSkillKlineDatasets(spaceID, _ string) (map[string]string, error) {
	return map[string]string{"1m": "dataset_" + spaceID + "_kline_1m"}, nil
}
