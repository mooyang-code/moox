package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealAndOpenRoundTripHidesChannelSecrets(t *testing.T) {
	store := &configcrypt.MemoryStore{}
	creds, err := DeriveCredentials(store, true)
	require.NoError(t, err)
	defer creds.Clear()
	require.GreaterOrEqual(t, len(creds.Passphrase()), 64)

	channels := []Channel{
		{ChannelID: "ch-1", ChannelType: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/secret-hook-1", Secret: "feishu-sign-secret", CreatedAt: 1700000000000},
		{ChannelID: "ch-2", ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=wecom-key"},
	}
	raw, err := Seal(`{"format":"moox.secrets/v1","ciphertext":"abc"}`, 2, channels, creds, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	// 落盘内容不含口令、Webhook 地址或签名密钥明文。
	text := string(raw)
	assert.NotContains(t, text, creds.Passphrase())
	assert.NotContains(t, text, "secret-hook-1")
	assert.NotContains(t, text, "feishu-sign-secret")
	assert.NotContains(t, text, "wecom-key")

	// 重新派生（create=false）能解开并核对条数。
	verifyCreds, err := DeriveCredentials(store, false)
	require.NoError(t, err)
	defer verifyCreds.Clear()
	assert.Equal(t, creds.Passphrase(), verifyCreds.Passphrase())

	opened, err := Open(raw, verifyCreds)
	require.NoError(t, err)
	assert.Equal(t, 2, opened.File.Secrets.Count)
	assert.Equal(t, `{"format":"moox.secrets/v1","ciphertext":"abc"}`, opened.File.Secrets.Bundle)
	assert.Equal(t, channels, opened.Channels)
}

func TestOpenRejectsTamperingAndForeignKey(t *testing.T) {
	store := &configcrypt.MemoryStore{}
	creds, err := DeriveCredentials(store, true)
	require.NoError(t, err)
	defer creds.Clear()

	raw, err := Seal(`{"format":"moox.secrets/v1"}`, 1, []Channel{{ChannelType: "webhook", WebhookURL: "https://example.com/hook", Secret: "s"}}, creds, time.Now())
	require.NoError(t, err)

	// 换另一台电脑的密钥：指纹不匹配，返回 ErrKeyMissing。
	otherCreds, err := DeriveCredentials(&configcrypt.MemoryStore{}, true)
	require.NoError(t, err)
	defer otherCreds.Clear()
	_, err = Open(raw, otherCreds)
	require.ErrorIs(t, err, configcrypt.ErrKeyMissing)

	// 篡改外层 count：AAD 不一致，解密失败。
	var tampered File
	require.NoError(t, json.Unmarshal(raw, &tampered))
	tampered.NotificationChannels.Count = 99
	tamperedRaw, err := json.Marshal(tampered)
	require.NoError(t, err)
	_, err = Open(tamperedRaw, creds)
	require.ErrorContains(t, err, "解密失败")
}

func TestWriteAtomicAndReadFileEnforcePermissionsAndRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.backup")
	require.NoError(t, WriteAtomic(path, []byte(`{"ok":true}`)))
	raw, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(raw))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		// 权限放宽后拒绝读取。
		require.NoError(t, os.Chmod(path, 0o644))
		_, err = ReadFile(path)
		require.ErrorContains(t, err, "0600")

		// 符号链接目标拒绝覆盖写入与读取。
		link := filepath.Join(dir, "link.backup")
		require.NoError(t, os.Symlink(path, link))
		err = WriteAtomic(link, []byte("x"))
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "普通文件"))
		_, err = ReadFile(link)
		require.Error(t, err)
	}

	// 覆盖写入新内容，无残留临时文件。
	require.NoError(t, WriteAtomic(path, []byte(`{"ok":2}`)))
	raw, err = ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":2}`, string(raw))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".moox.backup.tmp-"), "不应残留临时文件: %s", entry.Name())
	}

	// 不存在的文件返回错误。
	_, err = ReadFile(filepath.Join(dir, "missing.backup"))
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestPendingImportGuardAndLifecycle(t *testing.T) {
	store := &configcrypt.MemoryStore{}
	creds, err := DeriveCredentials(store, true)
	require.NoError(t, err)
	defer creds.Clear()

	dir := t.TempDir()
	path := filepath.Join(dir, "moox.backup")

	// 文件不存在时 ReadMetadata 与 MarkPendingImport 都返回 exists=false。
	meta, exists, err := ReadMetadata(path)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, meta)
	meta, exists, err = MarkPendingImport(path)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, meta)

	// 手动备份不设待导入标记，可以反复覆盖写入。
	manual1, err := Seal(`{"v":1}`, 1, []Channel{{ChannelType: "wecom", WebhookURL: "https://example.com/1"}}, creds, time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, WriteGuarded(path, manual1))
	manual2, err := Seal(`{"v":2}`, 2, []Channel{{ChannelType: "wecom", WebhookURL: "https://example.com/2"}}, creds, time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, WriteGuarded(path, manual2))

	meta, exists, err = ReadMetadata(path)
	require.NoError(t, err)
	require.True(t, exists)
	assert.False(t, meta.PendingImport)
	assert.Equal(t, 2, meta.Secrets.Count)

	// 打上待导入标记（例如 --no-backup 卸载沿用已有备份）后，仍可正常 Open 解密，且禁止被新备份覆盖。
	meta, exists, err = MarkPendingImport(path)
	require.NoError(t, err)
	require.True(t, exists)
	assert.True(t, meta.PendingImport)

	raw, err := ReadFile(path)
	require.NoError(t, err)
	opened, err := Open(raw, creds)
	require.NoError(t, err)
	assert.True(t, opened.File.PendingImport)
	require.Len(t, opened.Channels, 1)
	assert.Equal(t, "https://example.com/2", opened.Channels[0].WebhookURL)

	// 标记存在时 WriteGuarded 必须拒绝覆盖并保留原文件内容。
	overwriteAttempt, err := Seal(`{"v":3}`, 1, nil, creds, time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	err = WriteGuarded(path, overwriteAttempt)
	require.ErrorIs(t, err, ErrPendingImport)
	afterAttempt, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, raw, afterAttempt, "拒绝覆盖时原备份字节不能变")

	// 导入成功后清掉待导入标记并记录 imported_at，之后可再次正常覆盖写入。
	importedAt := time.Date(2026, 11, 28, 4, 0, 0, 0, time.UTC)
	require.NoError(t, MarkImported(path, importedAt))
	meta, exists, err = ReadMetadata(path)
	require.NoError(t, err)
	require.True(t, exists)
	assert.False(t, meta.PendingImport)
	assert.Equal(t, "2026-11-28T04:00:00Z", meta.ImportedAt)

	// 清掉标记后仍能 Open 解密，且允许新的备份覆盖写入。
	raw, err = ReadFile(path)
	require.NoError(t, err)
	opened, err = Open(raw, creds)
	require.NoError(t, err)
	assert.False(t, opened.File.PendingImport)

	nextRaw, err := Seal(`{"v":4}`, 4, nil, creds, time.Date(2026, 11, 28, 5, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, WriteGuarded(path, nextRaw))
	meta, exists, err = ReadMetadata(path)
	require.NoError(t, err)
	require.True(t, exists)
	assert.False(t, meta.PendingImport, "Seal 写出时不得自带待导入标记")
	assert.Equal(t, 4, meta.Secrets.Count)

	// ClearPendingImport 在无标记时为无操作；打标后可清除且备份仍能正常解密。
	cleared, err := ClearPendingImport(path)
	require.NoError(t, err)
	assert.False(t, cleared)

	_, _, err = MarkPendingImport(path)
	require.NoError(t, err)
	cleared, err = ClearPendingImport(path)
	require.NoError(t, err)
	assert.True(t, cleared)

	raw, err = ReadFile(path)
	require.NoError(t, err)
	opened, err = Open(raw, creds)
	require.NoError(t, err)
	assert.False(t, opened.File.PendingImport)
}

func TestVerifyKeyFingerprint(t *testing.T) {
	store := &configcrypt.MemoryStore{}
	creds, err := DeriveCredentials(store, true)
	require.NoError(t, err)
	defer creds.Clear()

	raw, err := Seal(`{"v":1}`, 1, nil, creds, time.Date(2026, 11, 28, 6, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "moox.backup")
	require.NoError(t, WriteAtomic(path, raw))
	meta, exists, err := ReadMetadata(path)
	require.NoError(t, err)
	require.True(t, exists)

	// 同一钥匙串通过校验。
	require.NoError(t, VerifyKeyFingerprint(meta, store))

	// 空钥匙串（没有主密钥）或另一把主密钥都必须报 ErrKeyFingerprintMismatch。
	require.ErrorIs(t, VerifyKeyFingerprint(meta, &configcrypt.MemoryStore{}), ErrKeyFingerprintMismatch)
	otherStore := &configcrypt.MemoryStore{}
	otherCreds, err := DeriveCredentials(otherStore, true)
	require.NoError(t, err)
	defer otherCreds.Clear()
	require.ErrorIs(t, VerifyKeyFingerprint(meta, otherStore), ErrKeyFingerprintMismatch)
}
