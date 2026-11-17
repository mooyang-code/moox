package configinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
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

// 秘密文件默认整份打码，只有用来辨认是哪一项的键（用户名、渠道类型、交易所密钥的名称、交易所、环境、只读）看得见：
// 告警 Webhook 地址里带着企业微信的 key、钉钉的 access_token，不能明文打到终端或 Agent 对话里。
func TestGetConfigMasksTheWholeSecretsFileExceptIdentifyingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, topology.SecretsFileName)
	const webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=wecom-key-123"
	plain := `[admin]
username = "admin"
password = "Adm1n-P@ss"

[tencent_cloud]
secret_id = "AKIDexample"
secret_key = "sk-example"

[notification]
channel_type = "wecom"
webhook_url = "` + webhook + `"

[[exchange_keys]]
name = "main"
exchange = "binance"
environment = "production"
api_key = "api-example"
secret_key = "secret-example"
readonly = true
`
	require.NoError(t, os.WriteFile(path, []byte(plain), 0o600))
	keys := &configcrypt.MemoryStore{}

	whole, err := GetConfig(path, "", false, keys)
	require.NoError(t, err)
	encoded, err := json.Marshal(whole)
	require.NoError(t, err)
	for _, secret := range []string{webhook, "wecom-key-123", "Adm1n-P@ss", "AKIDexample", "sk-example", "api-example", "secret-example"} {
		require.NotContains(t, string(encoded), secret)
	}
	notification, err := GetConfig(path, "notification", false, keys)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"channel_type": "wecom", "webhook_url": MaskedValue}, notification)
	url, err := GetConfig(path, "notification.webhook_url", false, keys)
	require.NoError(t, err)
	require.Equal(t, MaskedValue, url)
	exchange, err := GetConfig(path, "exchange_keys.0", false, keys)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"name": "main", "exchange": "binance", "environment": "production", "api_key": MaskedValue, "secret_key": MaskedValue, "readonly": true}, exchange)

	url, err = GetConfig(path, "notification.webhook_url", true, keys)
	require.NoError(t, err)
	require.Equal(t, webhook, url, "--reveal 才明文")
}

// config 各命令读秘密文件用 CLI 同一套校验：符号链接、不属于本人的文件都拒绝。
func TestConfigCommandsRefuseSymlinkedOrForeignSecretsFiles(t *testing.T) {
	configPath, secretsPath, keys := pairFixture(t)
	real := filepath.Join(t.TempDir(), "elsewhere.toml")
	raw, err := os.ReadFile(secretsPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(real, raw, 0o600))
	require.NoError(t, os.Remove(secretsPath))
	require.NoError(t, os.Symlink(real, secretsPath))
	_, err = GetConfig(secretsPath, "admin", false, keys)
	require.ErrorContains(t, err, "普通文件")
	_, err = SetConfig(secretsPath, "admin.username", "ops", false, keys, nil)
	require.ErrorContains(t, err, "普通文件")
	_, err = readConfigFile(secretsPath, true)
	require.ErrorContains(t, err, "普通文件")
	_ = configPath

	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时找不到不属于本人的文件")
	}
	foreign := "/etc/hosts"
	if _, err := os.Lstat(foreign); err != nil {
		t.Skip("没有可用的系统文件")
	}
	_, err = readConfigFile(foreign, true)
	require.ErrorContains(t, err, "属于当前用户")
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
