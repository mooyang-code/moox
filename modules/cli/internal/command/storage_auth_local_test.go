package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testPrimarySecret = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"

// 本机副本：0600、内容固定为一行、重复保存不改动；密钥太短或含非法字符被拒绝。
func TestSaveOperatorStorageAuth(t *testing.T) {
	t.Setenv("MOOX_OPERATOR_DIR", filepath.Join(t.TempDir(), "moox"))
	path, err := saveOperatorStorageAuth(testPrimarySecret)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET="+testPrimarySecret+"\n", string(raw))

	before := info.ModTime()
	_, err = saveOperatorStorageAuth(testPrimarySecret)
	require.NoError(t, err)
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before, after.ModTime(), "内容没变不重写")

	for _, bad := range []string{"", "short", strings.Repeat("a", 31), strings.Repeat("a", 40) + " x", strings.Repeat("a", 40) + "\n"[:0] + "$"} {
		_, err := saveOperatorStorageAuth(bad)
		require.Error(t, err, bad)
	}
	// 密钥轮换后覆盖旧值。
	_, err = saveOperatorStorageAuth(strings.Repeat("z", 40))
	require.NoError(t, err)
	raw, _ = os.ReadFile(path)
	require.Contains(t, string(raw), strings.Repeat("z", 40))
}

// 解析顺序：环境变量 → 命令行指定的文件 → 本机副本；都没有时给出可操作的提示。
func TestResolveStoragePrimarySecretOrder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOOX_OPERATOR_DIR", filepath.Join(dir, "operator"))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	explicit := filepath.Join(dir, "explicit.env")

	_, err := resolveStoragePrimarySecret(explicit)
	require.ErrorContains(t, err, "moox-cli setup storage-auth")

	_, err = saveOperatorStorageAuth(testPrimarySecret)
	require.NoError(t, err)
	secret, err := resolveStoragePrimarySecret(explicit)
	require.NoError(t, err)
	require.Equal(t, testPrimarySecret, secret, "没有指定文件时用本机副本")

	explicitSecret := strings.Repeat("e", 40)
	require.NoError(t, os.WriteFile(explicit, []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET="+explicitSecret+"\n"), 0o600))
	secret, err = resolveStoragePrimarySecret(explicit)
	require.NoError(t, err)
	require.Equal(t, explicitSecret, secret, "命令行指定的文件优先于本机副本")

	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", strings.Repeat("v", 40))
	secret, err = resolveStoragePrimarySecret(explicit)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("v", 40), secret, "环境变量优先级最高")

	// 文件存在但内容无效：报错，不悄悄换下一个来源。
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	require.NoError(t, os.WriteFile(explicit, []byte("garbage\n"), 0o600))
	_, err = resolveStoragePrimarySecret(explicit)
	require.ErrorContains(t, err, "无效")
	require.NotContains(t, err.Error(), "garbage")
}

// storagePrimaryRoleAuth 经本机副本也能得到鉴权信息（导入、导出命令的入口）。
func TestStoragePrimaryRoleAuthUsesOperatorCopy(t *testing.T) {
	t.Setenv("MOOX_OPERATOR_DIR", filepath.Join(t.TempDir(), "operator"))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	_, err := saveOperatorStorageAuth(testPrimarySecret)
	require.NoError(t, err)
	auth, err := storagePrimaryRoleAuth("secrets/storage-internal-auth.env", "moox-cli-data-import")
	require.NoError(t, err)
	require.Equal(t, "moox-cli-data-import", auth.GetAppId())
	require.NotEmpty(t, auth.GetAppKey())
	require.NotContains(t, auth.GetAppKey(), testPrimarySecret)
}
