package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exchangeKeysBody = `
[[exchange_keys]]
name = "readonly"
exchange = "binance"
environment = "production"
api_key = "ro-key"
secret_key = "ro-secret"
readonly = true

[[exchange_keys]]
name = "main"
exchange = "OKX"
environment = "Testnet"
api_key = "okx-key"
secret_key = "okx-secret"
passphrase = "okx-pass"
`

func TestLoadParsesExchangeKeysAndBuildsFixedIDs(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest+exchangeKeysBody, 0o600), root)
	require.NoError(t, err)
	keys := snapshot.Manifest.ExchangeKeys
	require.Len(t, keys, 2)
	assert.Equal(t, ExchangeKey{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "ro-key", SecretKey: "ro-secret", ReadOnly: true}, keys[0])
	// 交易所、环境小写化；okx 的 passphrase 保留。
	assert.Equal(t, "okx", keys[1].Exchange)
	assert.Equal(t, "testnet", keys[1].Environment)
	assert.Equal(t, "okx-pass", keys[1].Passphrase)
	assert.False(t, keys[1].ReadOnly, "readonly 缺省为 false")
	// Collector 检测币安现货下架用的固定 ID 必须正好是这个。
	assert.Equal(t, "exchange-binance-production-readonly", keys[0].ID())
	assert.Equal(t, "exchange-okx-testnet-main", keys[1].ID())
}

// 没写 [[exchange_keys]] 的配置照常加载；注释掉的样例不算数，不会被 apply 进 SecretMgr。
func TestLoadWithoutExchangeKeysAndCommentedSample(t *testing.T) {
	root := t.TempDir()
	commented := validManifest + `
# [[exchange_keys]]
# name = "readonly"
# exchange = "binance"
# environment = "production"
# api_key = "填入币安只读 API Key"
# secret_key = "填入币安只读 Secret Key"
# readonly = true
`
	snapshot, err := Load(writeManifest(t, root, commented, 0o600), root)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Manifest.ExchangeKeys)
}

func TestLoadRejectsInvalidExchangeKeys(t *testing.T) {
	block := func(extra string) string {
		return validManifest + "\n[[exchange_keys]]\nname = \"main\"\nexchange = \"binance\"\nenvironment = \"production\"\napi_key = \"k\"\nsecret_key = \"s\"\n" + extra
	}
	replace := func(old, new string) string { return strings.Replace(block(""), old, new, 1) }
	tests := []struct {
		name string
		body string
		want string
	}{
		{"名称含大写", replace(`name = "main"`, `name = "Main"`), "exchange_keys[0].name"},
		{"名称含下划线", replace(`name = "main"`, `name = "main_key"`), "exchange_keys[0].name"},
		{"名称为空", replace(`name = "main"`, `name = ""`), "exchange_keys[0].name"},
		{"交易所不认识", replace(`exchange = "binance"`, `exchange = "bybit"`), "exchange_keys[0].exchange"},
		{"环境不认识", replace(`environment = "production"`, `environment = "paper"`), "exchange_keys[0].environment"},
		{"缺 api_key", replace(`api_key = "k"`, `api_key = ""`), "api_key 与 secret_key"},
		{"缺 secret_key", replace(`secret_key = "s"`, `secret_key = ""`), "api_key 与 secret_key"},
		{"OKX 缺 passphrase", replace(`exchange = "binance"`, `exchange = "okx"`), "passphrase"},
		{"固定 ID 重复", block("") + "\n[[exchange_keys]]\nname = \"main\"\nexchange = \"Binance\"\nenvironment = \"production\"\napi_key = \"k2\"\nsecret_key = \"s2\"\n", "重复"},
		{"未知键仍然报错", block("unknown_field = 1\n"), "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, tt.body, 0o600), root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			// 报错不能带出密钥值。
			assert.NotContains(t, err.Error(), "okx-secret")
		})
	}

	// 同名但交易所不同是两把不同的密钥。
	root := t.TempDir()
	body := block("") + "\n[[exchange_keys]]\nname = \"main\"\nexchange = \"okx\"\nenvironment = \"production\"\napi_key = \"k2\"\nsecret_key = \"s2\"\npassphrase = \"p\"\n"
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Len(t, snapshot.Manifest.ExchangeKeys, 2)
}

// 首次安装后再加密钥不能触发 deployment_config_changed。
func TestExchangeKeysAreNotPartOfTheDeploymentDigest(t *testing.T) {
	root := t.TempDir()
	without, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	root2 := t.TempDir()
	with, err := Load(writeManifest(t, root2, validManifest+exchangeKeysBody, 0o600), root2)
	require.NoError(t, err)
	assert.Equal(t, without.DeploymentDigest(), with.DeploymentDigest())
}
