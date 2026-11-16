package configinit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

func TestGetConfigMasksSecretsUnlessRevealed(t *testing.T) {
	configPath, secretsPath, keys := pairFixture(t)
	const secret = "Adm1n-P@ss"

	admin, err := GetConfig(secretsPath, "admin", false, keys)
	require.NoError(t, err)
	require.Equal(t, MaskedValue, admin.(map[string]any)["password"])
	require.NotEmpty(t, admin.(map[string]any)["username"])

	pw, err := GetConfig(secretsPath, "admin.password", false, keys)
	require.NoError(t, err)
	require.Equal(t, MaskedValue, pw)

	pw, err = GetConfig(secretsPath, "admin.password", true, keys)
	require.NoError(t, err)
	require.Equal(t, secret, pw)

	whole, err := GetConfig(secretsPath, "", false, keys)
	require.NoError(t, err)
	encoded, err := json.Marshal(whole)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.NotContains(t, string(encoded), "key-example")

	// 在 moox.toml 里找秘密段：提示去秘密文件里找。
	_, err = GetConfig(configPath, "admin.password", false, keys)
	require.ErrorContains(t, err, "加 --file "+secretsPath)
}

func TestGetConfigRejectsUnknownPathAndIndexesArrays(t *testing.T) {
	path, _, keys := pairFixture(t)
	_, err := GetConfig(path, "nope.x", false, keys)
	require.ErrorContains(t, err, "没有 nope.x")
	_, err = GetConfig(path, "hosts.storage.label.x", false, keys)
	require.ErrorContains(t, err, "单个值")
	first, err := GetConfig(path, "deployments.control.0", false, keys)
	require.NoError(t, err)
	require.Equal(t, "admin", first)
	_, err = GetConfig(path, "deployments.control.99", false, keys)
	require.ErrorContains(t, err, "序号")
}

// moox.toml 是普通的明文读写；秘密文件改完重新加密。两者都只动那一行，并留下备份。
func TestSetConfigEditsOneLineAndKeepsEverythingElse(t *testing.T) {
	configPath, secretsPath, keys := pairFixture(t)
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	before, _ := os.ReadFile(configPath)

	report, err := SetConfig(configPath, "hosts.storage.label", "存储主机", false, keys, now)
	require.NoError(t, err)
	require.True(t, report.Changed && report.Valid && !report.Written)
	after, _ := os.ReadFile(configPath)
	require.Equal(t, before, after, "预览不写回")

	report, err = SetConfig(configPath, "hosts.storage.label", "存储主机", true, keys, now)
	require.NoError(t, err)
	require.True(t, report.Written)
	require.FileExists(t, report.Backup)
	after, _ = os.ReadFile(configPath)
	require.False(t, configcrypt.IsEncrypted(after), "moox.toml 写回后仍是明文")
	require.Equal(t, strings.Replace(string(before), `label = "存储机"`, `label = "存储主机"`, 1), string(after), "只有那一行变化")

	got, err := GetConfig(configPath, "hosts.storage.label", false, keys)
	require.NoError(t, err)
	require.Equal(t, "存储主机", got)

	sealedBefore, _ := os.ReadFile(secretsPath)
	report, err = SetConfig(secretsPath, "admin.username", "ops", true, keys, now)
	require.NoError(t, err)
	require.True(t, report.Written)
	sealedAfter, _ := os.ReadFile(secretsPath)
	require.True(t, configcrypt.IsEncrypted(sealedAfter), "秘密文件写回后重新加密")
	plainBefore, _ := configcrypt.Decrypt(sealedBefore, keys)
	plainAfter, err := configcrypt.Decrypt(sealedAfter, keys)
	require.NoError(t, err)
	require.Equal(t, strings.Replace(string(plainBefore), `username = "admin"`, `username = "ops"`, 1), string(plainAfter))
}

func TestSetConfigTypesAndRefusals(t *testing.T) {
	configPath, secretsPath, keys := pairFixture(t)
	_, err := SetConfig(configPath, "logging.cls.enabled", "false", false, keys, nil)
	require.NoError(t, err)
	_, err = SetConfig(configPath, "logging.nope", "1", false, keys, nil)
	require.ErrorContains(t, err, "只改已有的键")
	_, err = SetConfig(configPath, "nope.x", "1", false, keys, nil)
	require.ErrorContains(t, err, "只改已有的表")
	_, err = SetConfig(configPath, "deployments.control", "x", false, keys, nil)
	require.ErrorContains(t, err, "不能改")
	_, err = SetConfig(configPath, "hosts.storage.ssh", "x", false, keys, nil)
	require.ErrorContains(t, err, "不能改")
	_, err = SetConfig(configPath, "admin.username", "ops", false, keys, nil)
	require.ErrorContains(t, err, "加 --file "+secretsPath, "在 moox.toml 里改秘密段时提示去秘密文件")
	_, err = SetConfig(secretsPath, "admin.password", "", false, keys, nil)
	require.ErrorContains(t, err, "校验没有通过", "改完按秘密文件的格式校验")

	require.Equal(t, "true", tomlScalar("true"))
	require.Equal(t, "12", tomlScalar("12"))
	require.Equal(t, "1.5", tomlScalar("1.5"))
	require.Equal(t, `"240d"`, tomlScalar("240d"))
	require.Equal(t, `  # 说明`, trailingComment(`"a"  # 说明`))
	require.Equal(t, "", trailingComment(`"a#b"`))
}
