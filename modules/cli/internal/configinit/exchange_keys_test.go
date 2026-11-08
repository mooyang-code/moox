package configinit

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

func exchangeKeyRequest() Request {
	request := sampleRequest()
	request.ExchangeKeys = []ExchangeKeyEntry{
		{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "ro-key", SecretKey: "ro-secret", ReadOnly: true},
		{Name: "main", Exchange: " OKX ", Environment: "Testnet", APIKey: "okx-key", SecretKey: "okx-secret", Passphrase: "okx-pass"},
	}
	return request
}

// 示例里只有注释掉的样例：不填交易所密钥时渲染结果里没有 [[exchange_keys]]，占位值不会被 apply 进 SecretMgr。
func TestExampleHasOnlyCommentedExchangeKeySamples(t *testing.T) {
	assert.Contains(t, exampleTemplate, "# [[exchange_keys]]")
	for _, line := range strings.Split(exampleTemplate, "\n") {
		assert.NotEqual(t, "[[exchange_keys]]", strings.TrimSpace(line), "示例里的 [[exchange_keys]] 必须是注释")
	}
	manifest, text := renderWith(t, sampleRequest(), nil)
	assert.Empty(t, manifest.ExchangeKeys)
	assert.NotContains(t, text, "\n[[exchange_keys]]")
	// 样例注释原样留在生成的文件里，用户以后可以取消注释。
	assert.Contains(t, text, "# [[exchange_keys]]")
}

func TestRenderWritesExchangeKeysAsAnArrayTable(t *testing.T) {
	manifest, text := renderWith(t, exchangeKeyRequest(), nil)
	require.Len(t, manifest.ExchangeKeys, 2)
	assert.Equal(t, setupconfig.ExchangeKey{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "ro-key", SecretKey: "ro-secret", ReadOnly: true}, manifest.ExchangeKeys[0])
	// 交易所、环境小写化，passphrase 写进去。
	assert.Equal(t, "okx", manifest.ExchangeKeys[1].Exchange)
	assert.Equal(t, "testnet", manifest.ExchangeKeys[1].Environment)
	assert.Equal(t, "okx-pass", manifest.ExchangeKeys[1].Passphrase)
	assert.Equal(t, "exchange-binance-production-readonly", manifest.ExchangeKeys[0].ID())
	// 字段名是 config get 自动打码认的那三个。
	assert.Contains(t, text, "\napi_key = \"ro-key\"\nsecret_key = \"ro-secret\"\nreadonly = true\n")
	assert.Contains(t, text, "passphrase = \"okx-pass\"")
	assert.NotContains(t, text, "passphrase = \"\"")
	// 其它内容不受影响：主机、示例里的下一段说明仍然在。
	assert.Contains(t, text, "# setup init 导入因子")
	assert.True(t, strings.HasSuffix(text, "\n"))
}

// 没配海外服务器（Trade 所在的主机）时交易所密钥没有用途，录入页隐藏这一步；服务端再拦一道。
func TestRenderRejectsExchangeKeysWithoutAnOverseasServer(t *testing.T) {
	request := exchangeKeyRequest()
	request.Servers.Overseas = nil
	err := renderErr(request, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "海外服务器")
	assert.NotContains(t, err.Error(), "ro-secret")

	// 不带密钥时没有海外服务器照常渲染。
	request.ExchangeKeys = nil
	require.NoError(t, renderErr(request, nil))
}

func TestRenderRejectsInvalidExchangeKeysWithoutEchoingSecrets(t *testing.T) {
	for name, mutate := range map[string]func(*ExchangeKeyEntry){
		"名称含大写":     func(e *ExchangeKeyEntry) { e.Name = "Main" },
		"名称含空格":     func(e *ExchangeKeyEntry) { e.Name = "my key" },
		"交易所不认识":    func(e *ExchangeKeyEntry) { e.Exchange = "bybit" },
		"环境不认识":     func(e *ExchangeKeyEntry) { e.Environment = "paper" },
		"缺 api_key": func(e *ExchangeKeyEntry) { e.APIKey = "" },
		"OKX 缺口令":   func(e *ExchangeKeyEntry) { e.Exchange = "okx"; e.Passphrase = "" },
		"密钥含换行":     func(e *ExchangeKeyEntry) { e.SecretKey = "line1\nline2" },
	} {
		t.Run(name, func(t *testing.T) {
			request := sampleRequest()
			entry := ExchangeKeyEntry{Name: "main", Exchange: "binance", Environment: "production", APIKey: "recognizable-api-key", SecretKey: "recognizable-secret"}
			mutate(&entry)
			request.ExchangeKeys = []ExchangeKeyEntry{entry}
			err := renderErr(request, nil)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "recognizable")
		})
	}

	// 固定 ID 重复：同一交易所、环境下名称不能相同。
	request := sampleRequest()
	entry := ExchangeKeyEntry{Name: "main", Exchange: "binance", Environment: "production", APIKey: "k", SecretKey: "s"}
	request.ExchangeKeys = []ExchangeKeyEntry{entry, entry}
	err := renderErr(request, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "重复")
}

const blocksFixture = `[notification]
channel_type = "wecom"

# 交易所密钥样例
# [[exchange_keys]]
# name = "x"

[[exchange_keys]]
name = "old-a"
exchange = "binance"
environment = "production"
api_key = "a"
secret_key = "b"

[[exchange_keys]]
name = "old-b"
exchange = "binance"
environment = "testnet"
api_key = "c"
secret_key = "d"

# 因子段的说明，属于下一个段，删除交易所密钥时不能一起删掉
# 第二行说明
[factors]
enabled = true
`

func replaced(entries []ExchangeKeyEntry) string {
	return strings.Join(replaceExchangeKeys(strings.Split(blocksFixture, "\n"), entries), "\n")
}

func TestReplaceExchangeKeysKeepsTheCommentsOfTheNextSection(t *testing.T) {
	// 全部删除：下一段的说明注释仍在，前面的样例注释也在，空行恰好一个。
	out := replaced([]ExchangeKeyEntry{})
	assert.NotContains(t, out, "old-a")
	assert.NotContains(t, out, "old-b")
	assert.Contains(t, out, "# 因子段的说明，属于下一个段，删除交易所密钥时不能一起删掉\n# 第二行说明\n[factors]\nenabled = true\n")
	assert.Contains(t, out, "# 交易所密钥样例\n# [[exchange_keys]]\n# name = \"x\"\n")
	assert.NotContains(t, out, "\n\n\n")
	assert.True(t, strings.HasSuffix(out, "enabled = true\n"))
	manifestText := out
	assert.NotContains(t, manifestText, "\n[[exchange_keys]]")
}

func TestReplaceExchangeKeysAddsEditsAndRemovesAtTheOldPosition(t *testing.T) {
	// 改一把、加一把、删一把：新块写在第一个旧块的位置，下一段说明仍紧跟其后。
	out := replaced([]ExchangeKeyEntry{
		{Name: "old-a", Exchange: "binance", Environment: "production", APIKey: "a2", SecretKey: "b2", ReadOnly: true},
		{Name: "fresh", Exchange: "okx", Environment: "production", APIKey: "k", SecretKey: "s", Passphrase: "p"},
	})
	assert.Contains(t, out, "api_key = \"a2\"")
	assert.Contains(t, out, "readonly = true")
	assert.Contains(t, out, "name = \"fresh\"")
	assert.NotContains(t, out, "old-b")
	assert.NotContains(t, out, "api_key = \"a\"\n")
	assert.Less(t, strings.Index(out, "name = \"old-a\""), strings.Index(out, "name = \"fresh\""))
	assert.Less(t, strings.Index(out, "name = \"fresh\""), strings.Index(out, "# 因子段的说明"))
	assert.Less(t, strings.Index(out, "# [[exchange_keys]]"), strings.Index(out, "name = \"old-a\""))
	assert.Contains(t, out, "passphrase = \"p\"\n\n# 因子段的说明")
	assert.NotContains(t, out, "\n\n\n")
	// 结果是合法的 TOML 数组段：两个块。
	blocks := 0
	for _, b := range splitBlocks(strings.Split(out, "\n")) {
		if b.header == "exchange_keys" && b.array {
			blocks++
		}
	}
	assert.Equal(t, 2, blocks)
}

func TestReplaceExchangeKeysWithNoExistingBlockAppendsAtTheEnd(t *testing.T) {
	lines := strings.Split("[a]\nx = 1\n\n# 末尾的注释\n", "\n")
	assert.Equal(t, lines, replaceExchangeKeys(lines, nil), "没有旧块也没有新块时原样返回")
	out := strings.Join(replaceExchangeKeys(lines, []ExchangeKeyEntry{{Name: "m", Exchange: "binance", Environment: "production", APIKey: "k", SecretKey: "s"}}), "\n")
	assert.Equal(t, "[a]\nx = 1\n\n# 末尾的注释\n\n[[exchange_keys]]\nname = \"m\"\nexchange = \"binance\"\nenvironment = \"production\"\napi_key = \"k\"\nsecret_key = \"s\"\n", out)
	// 最后一个块被删掉后文件仍以单个换行结尾。
	last := strings.Split("[a]\nx = 1\n\n[[exchange_keys]]\nname = \"m\"\n", "\n")
	assert.Equal(t, "[a]\nx = 1\n", strings.Join(replaceExchangeKeys(last, []ExchangeKeyEntry{}), "\n"))
}

// config get：路径用序号，api_key、secret_key、passphrase 自动打码，其余字段原样。
func TestGetConfigMasksExchangeKeys(t *testing.T) {
	_, text := renderWith(t, exchangeKeyRequest(), nil)
	path, _, keys := migrateFixture(t, text)

	first, err := GetConfig(path, "exchange_keys.0", false, keys)
	require.NoError(t, err)
	node := first.(map[string]any)
	assert.Equal(t, MaskedValue, node["api_key"])
	assert.Equal(t, MaskedValue, node["secret_key"])
	assert.Equal(t, "readonly", node["name"])
	assert.Equal(t, true, node["readonly"])
	second, err := GetConfig(path, "exchange_keys.1.passphrase", false, keys)
	require.NoError(t, err)
	assert.Equal(t, MaskedValue, second)
	revealed, err := GetConfig(path, "exchange_keys.1.secret_key", true, keys)
	require.NoError(t, err)
	assert.Equal(t, "okx-secret", revealed)

	whole, err := GetConfig(path, "", false, keys)
	require.NoError(t, err)
	assert.NotContains(t, asJSON(t, whole), "okx-secret")
	assert.NotContains(t, asJSON(t, whole), "ro-key")

	// config set 不能改数组表，要用 config edit。
	_, err = SetConfig(path, "exchange_keys.api_key", "x", false, keys, nil)
	require.ErrorContains(t, err, "数组表")
}

func asJSON(t *testing.T, value any) string {
	t.Helper()
	text, err := jsonMarshal(value)
	require.NoError(t, err)
	return text
}

func TestEditorOverviewMasksAndRevealsExchangeKeysByIndex(t *testing.T) {
	_, text := renderWith(t, exchangeKeyRequest(), nil)
	h := startEditor(t, []byte(text))

	status, reply, raw := h.call("config/overview", struct{}{}, nil)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, reply.Overview.ExchangeKeys, 2)
	view := reply.Overview.ExchangeKeys[0]
	assert.Equal(t, "exchange-binance-production-readonly", view.SecretID)
	assert.True(t, view.ReadOnly)
	assert.False(t, view.HasPassphrase)
	assert.True(t, reply.Overview.ExchangeKeys[1].HasPassphrase)
	for _, secret := range []string{"ro-key", "ro-secret", "okx-key", "okx-secret", "okx-pass"} {
		assert.NotContains(t, raw, secret, "概览里不能有密钥值")
	}

	for field, want := range map[string]string{
		"exchange_keys.0.api_key": "ro-key", "exchange_keys.0.secret_key": "ro-secret",
		"exchange_keys.1.api_key": "okx-key", "exchange_keys.1.secret_key": "okx-secret", "exchange_keys.1.passphrase": "okx-pass",
	} {
		status, reply, _ := h.call("config/reveal", map[string]string{"field": field}, nil)
		require.Equal(t, http.StatusOK, status, field)
		assert.Equal(t, want, reply.Value, field)
	}
	for _, field := range []string{"exchange_keys.2.api_key", "exchange_keys.-1.api_key", "exchange_keys.x.api_key", "exchange_keys.0", "exchange_keys.0.name", "exchange_keys.0.api_key.x"} {
		status, _, _ := h.call("config/reveal", map[string]string{"field": field}, nil)
		assert.Equal(t, http.StatusNotFound, status, field)
	}
}

func TestEditorFormEditsExchangeKeysAsAWhole(t *testing.T) {
	_, text := renderWith(t, exchangeKeyRequest(), nil)
	h := startEditor(t, []byte(text))
	_, reply, _ := h.call("config/form", struct{}{}, nil)
	form := *reply.Form
	require.Len(t, form.ExchangeKeys, 2)
	assert.Equal(t, "ro-secret", form.ExchangeKeys[0].SecretKey, "表单只在本机页面里带明文")

	// 改一把、加一把、删一把。
	form.ExchangeKeys = []ExchangeKeyEntry{
		{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "ro-key-2", SecretKey: "ro-secret-2", ReadOnly: true},
		{Name: "trade", Exchange: "binance", Environment: "testnet", APIKey: "t-key", SecretKey: "t-secret"},
	}
	status, saved, raw := h.call("config/update", form, nil)
	require.Equal(t, http.StatusOK, status, raw)
	manifest, err := setupconfig.Parse([]byte(h.plain()))
	require.NoError(t, err)
	require.Len(t, manifest.ExchangeKeys, 2)
	assert.Equal(t, "ro-key-2", manifest.ExchangeKeys[0].APIKey)
	assert.True(t, manifest.ExchangeKeys[0].ReadOnly)
	assert.Equal(t, "trade", manifest.ExchangeKeys[1].Name)
	require.Len(t, saved.Overview.ExchangeKeys, 2)
	assert.NotContains(t, raw, "t-secret", "保存结果里不能有密钥值")
	assert.Contains(t, h.plain(), "# setup init 导入因子", "整块替换不能删掉下一段的说明")

	// 校验不过时拒绝保存，文件不变。
	before := h.plain()
	bad := form
	bad.ExchangeKeys = []ExchangeKeyEntry{{Name: "Bad Name", Exchange: "binance", Environment: "production", APIKey: "k", SecretKey: "s"}}
	status, reply2, _ := h.call("config/update", bad, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, reply2.Message, "没有保存")
	assert.NotContains(t, reply2.Message, "Bad Name")
	assert.Equal(t, before, h.plain())

	// 空列表表示全部删除；不带这个字段（nil）则保持原样。
	form.ExchangeKeys = nil
	status, _, _ = h.call("config/update", form, nil)
	require.Equal(t, http.StatusOK, status)
	manifest, err = setupconfig.Parse([]byte(h.plain()))
	require.NoError(t, err)
	assert.Len(t, manifest.ExchangeKeys, 2, "字段缺省时不能把密钥清掉")
	form.ExchangeKeys = []ExchangeKeyEntry{}
	status, _, _ = h.call("config/update", form, nil)
	require.Equal(t, http.StatusOK, status)
	manifest, err = setupconfig.Parse([]byte(h.plain()))
	require.NoError(t, err)
	assert.Empty(t, manifest.ExchangeKeys)
	assert.Contains(t, h.plain(), "# [[exchange_keys]]", "注释样例还在")
	_, err = os.Stat(h.path)
	require.NoError(t, err)
}

func jsonMarshal(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}
