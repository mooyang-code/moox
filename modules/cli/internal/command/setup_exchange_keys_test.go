package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exchangeKeysManifestBody = `[admin]
username = "admin"
password = "admin-test-password"

[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"

[eventbus]
port = 4333
tls_enabled = true

[notification]
channel_type = "wecom"
webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"

[hosts.control]
address = "203.0.113.8"
[hosts.control.ssh]
port = 22
username = "ubuntu"
password = "control-ssh-password"

[hosts.overseas]
address = "203.0.113.9"
[hosts.overseas.ssh]
port = 22
username = "ubuntu"
password = "other-ssh-password"

[deployments]
control = ["admin", "console-proxy", "web-host", "eventbus", "collector", "factor-mgr", "archive-mgr", "storage-primary"]
overseas = %s

[[exchange_keys]]
name = "readonly"
exchange = "binance"
environment = "production"
api_key = "recognizable-ro-key"
secret_key = "recognizable-ro-secret"
readonly = true

[[exchange_keys]]
name = "main"
exchange = "binance"
environment = "production"
api_key = "recognizable-main-key"
secret_key = "recognizable-main-secret"

[[exchange_keys]]
name = "revoked"
exchange = "okx"
environment = "testnet"
api_key = "recognizable-okx-key"
secret_key = "recognizable-okx-secret"
passphrase = "recognizable-passphrase"
`

func exchangeKeysSnapshot(t *testing.T, overseas string) *setupconfig.Snapshot {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.toml")
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(exchangeKeysManifestBody, "%s", overseas, 1)), 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	return snapshot
}

func appliedExchangeKeys() []setupclient.ExchangeKeyResult {
	return []setupclient.ExchangeKeyResult{
		{SecretID: "exchange-binance-production-readonly", State: setupclient.ExchangeKeyCreated, ReadOnly: true, Note: "已写入密钥管理"},
		{SecretID: "exchange-binance-production-main", State: setupclient.ExchangeKeyExists, Note: "已存在，以管理台为准，未覆盖"},
		{SecretID: "exchange-okx-testnet-revoked", State: setupclient.ExchangeKeyDeleted, Note: "已在管理台删除，未重建"},
	}
}

func runSetupApply(t *testing.T, snapshot *setupconfig.Snapshot, inspect func(context.Context, *setupconfig.Snapshot, setupconfig.ExchangeKey) ([]byte, error)) (map[string]any, string) {
	t.Helper()
	deps := setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		validate: func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
			return setupvalidate.Result{}, nil
		},
		apply: func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
			return setupclient.ApplyResult{Action: "created", Users: 1, Secrets: 4, ExchangeKeys: appliedExchangeKeys()}, nil
		},
		login: func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			return setupclient.LoginResult{LoginAPI: "valid"}, nil
		},
		inspectExchangeKey: inspect,
	}
	cmd := newSetupCommand(deps)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"apply", "--file", "moox.toml"})
	require.NoError(t, cmd.Execute(), "检测结果不能让 apply 以非 0 退出")
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), "输出必须仍是一个 JSON 文档")
	return result, stdout.String() + stderr.String()
}

func checksByID(t *testing.T, result map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	items, _ := result["exchange_key_checks"].([]any)
	for _, item := range items {
		check := item.(map[string]any)
		out[check["secret_id"].(string)] = check
	}
	return out
}

const inspectionPassed = `{"ret_info":{"code":"SUCCESS","msg":"success"},"inspection":{"account_mode":"BINANCE_STANDARD","available_markets":["MARKET_TYPE_SPOT","MARKET_TYPE_SWAP"],"blockers":[],"warnings":[{"code":"w","message":"没有绑定 IP"}]}}`

func TestSetupApplyInspectsEachWrittenKeyThroughTrade(t *testing.T) {
	snapshot := exchangeKeysSnapshot(t, `["trade", "access", "egress-proxy"]`)
	var inspected []setupconfig.ExchangeKey
	result, output := runSetupApply(t, snapshot, func(_ context.Context, _ *setupconfig.Snapshot, key setupconfig.ExchangeKey) ([]byte, error) {
		inspected = append(inspected, key)
		return []byte(inspectionPassed), nil
	})

	// 只读的、已在管理台删除的不检测，只检测 main 这一把。
	require.Len(t, inspected, 1)
	assert.Equal(t, "exchange-binance-production-main", inspected[0].ID())

	checks := checksByID(t, result)
	require.Len(t, checks, 3)
	assert.Equal(t, "skipped", checks["exchange-binance-production-readonly"]["status"])
	assert.Contains(t, checks["exchange-binance-production-readonly"]["message"], "只读")
	assert.Equal(t, "skipped", checks["exchange-okx-testnet-revoked"]["status"])
	assert.Contains(t, checks["exchange-okx-testnet-revoked"]["message"], "已在管理台删除")
	main := checks["exchange-binance-production-main"]
	assert.Equal(t, "ok", main["status"])
	assert.Equal(t, "BINANCE_STANDARD", main["account_mode"])
	assert.Equal(t, []any{"没有绑定 IP"}, main["warnings"])
	assert.Equal(t, []any{"MARKET_TYPE_SPOT", "MARKET_TYPE_SWAP"}, main["available_markets"])

	// apply 本身的结果照常输出，并带每把密钥的状态。
	assert.Equal(t, "valid", result["login_api"])
	assert.Equal(t, "created", result["action"])
	assert.Len(t, result["exchange_keys"], 3)

	for _, value := range []string{"recognizable-ro-key", "recognizable-ro-secret", "recognizable-main-key", "recognizable-main-secret", "recognizable-okx-secret", "recognizable-passphrase"} {
		assert.NotContains(t, output, value, "输出里不能有密钥值")
	}
}

func TestSetupApplyOnlyHintsWhenTradeIsNotDeployed(t *testing.T) {
	// moox.toml 的 [deployments] 里没有 trade：全新安装时 apply 排在 Trade 部署之前。
	snapshot := exchangeKeysSnapshot(t, `["access", "egress-proxy"]`)
	calls := 0
	result, _ := runSetupApply(t, snapshot, func(context.Context, *setupconfig.Snapshot, setupconfig.ExchangeKey) ([]byte, error) {
		calls++
		return []byte(inspectionPassed), nil
	})
	assert.Zero(t, calls, "Trade 没部署时不调用检测")
	main := checksByID(t, result)["exchange-binance-production-main"]
	assert.Equal(t, "pending", main["status"])
	message := main["message"].(string)
	assert.Contains(t, message, "moox-cli trade inspect-credential")
	assert.Contains(t, message, "--credential-secret-id exchange-binance-production-main")
	assert.Contains(t, message, "EXCHANGE_BINANCE")
	assert.Contains(t, message, "ACCOUNT_ENVIRONMENT_PRODUCTION")
	assert.Contains(t, message, "管理台")
	// 只读和已删除的仍然是跳过，不提示检测。
	assert.Equal(t, "skipped", checksByID(t, result)["exchange-binance-production-readonly"]["status"])
}

func TestSetupApplyShowsBlockersWithoutFailing(t *testing.T) {
	snapshot := exchangeKeysSnapshot(t, `["trade", "access", "egress-proxy"]`)
	result, _ := runSetupApply(t, snapshot, func(context.Context, *setupconfig.Snapshot, setupconfig.ExchangeKey) ([]byte, error) {
		return []byte(`{"ret_info":{"code":"SUCCESS"},"inspection":{"account_mode":"BINANCE_PORTFOLIO_MARGIN","blockers":[{"code":"withdraw","message":"密钥开了提现权限"},{"code":"ip","message":"没有绑定 IP"}]}}`), nil
	})
	main := checksByID(t, result)["exchange-binance-production-main"]
	assert.Equal(t, "blocked", main["status"])
	assert.Equal(t, []any{"密钥开了提现权限", "没有绑定 IP"}, main["blockers"])
}

func TestClassifyInspectionByReturnCode(t *testing.T) {
	key := setupconfig.ExchangeKey{Name: "main", Exchange: "okx", Environment: "testnet"}
	tests := []struct {
		name   string
		raw    string
		err    error
		status string
		want   string
	}{
		{"检测没有完成只提示稍后重试", `{"ret_info":{"code":"INNER_ERR","msg":"trade account: 密钥检测没有完成：timeout"}}`, nil, "incomplete", "稍后重试"},
		{"INNER_ERR 不说成交易所不可用，指向 Trade 日志", `{"ret_info":{"code":"INNER_ERR","msg":"读取密钥失败"}}`, nil, "incomplete", "查看 Trade 日志"},
		{"数字返回码 4 也是没有完成", `{"ret_info":{"code":4,"msg":"x"}}`, nil, "incomplete", "不能断定密钥有问题"},
		{"密钥读不到", `{"ret_info":{"code":"NOT_FOUND","msg":"秘钥不存在"}}`, nil, "failed", "密钥不可用：秘钥不存在"},
		{"参数错误", `{"ret_info":{"code":"INVALID_PARAM","msg":"秘钥未启用"}}`, nil, "failed", "秘钥未启用"},
		{"调用失败视为 Trade 未就绪", ``, errors.New("route not found"), "pending", "moox-cli trade inspect-credential"},
		{"看不懂的返回", `not json`, nil, "incomplete", "稍后重试"},
		{"测试网跳过检测", `{"ret_info":{"code":"SUCCESS"},"inspection":{"account_mode":"BINANCE_STANDARD","skipped":true}}`, nil, "ok", "测试网跳过"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := classifyInspection("exchange-okx-testnet-main", key, []byte(tt.raw), tt.err)
			assert.Equal(t, tt.status, check.Status)
			assert.Contains(t, check.Message, tt.want)
			if tt.status == "incomplete" || tt.status == "pending" {
				assert.Contains(t, check.Message, "--credential-secret-id exchange-okx-testnet-main", "给出可以复制的检测命令")
				assert.Contains(t, check.Message, "ACCOUNT_ENVIRONMENT_TESTNET")
			}
		})
	}
}

func TestTradeInspectRequestUsesFixedIDAndEnumNames(t *testing.T) {
	request := tradeInspectRequest(setupconfig.ExchangeKey{Name: "readonly", Exchange: "binance", Environment: "production"})
	assert.Equal(t, map[string]any{
		"exchange": "EXCHANGE_BINANCE", "environment": "ACCOUNT_ENVIRONMENT_PRODUCTION", "credential_secret_id": "exchange-binance-production-readonly",
	}, request)
	assert.Equal(t, "ACCOUNT_ENVIRONMENT_TESTNET", tradeInspectRequest(setupconfig.ExchangeKey{Name: "m", Exchange: "okx", Environment: "testnet"})["environment"])
}

// 检测走的是 trade inspect-credential 命令已经使用的方法和字段，两边保持一致。
func TestInspectRequestMatchesTheInspectCredentialCommand(t *testing.T) {
	var spec *rpcSpec
	for i := range rpcSpecs {
		if strings.Join(rpcSpecs[i].Path, " ") == "trade inspect-credential" {
			spec = &rpcSpecs[i]
		}
	}
	require.NotNil(t, spec)
	assert.Equal(t, tradeConsole, spec.Service)
	assert.Equal(t, "InspectExchangeCredential", spec.Method)
	fields := map[string]bool{}
	for _, flag := range spec.Flags {
		fields[flag.Field] = true
	}
	for field := range tradeInspectRequest(setupconfig.ExchangeKey{Name: "m", Exchange: "okx", Environment: "testnet"}) {
		assert.True(t, fields[field], "命令没有字段 %s", field)
	}
}

// 海外机连不上 Trade：第一把密钥的检测超时后，其余密钥不再逐把等超时。
func TestSetupApplyStopsCallingTradeAfterFirstUnreachable(t *testing.T) {
	snapshot := exchangeKeysSnapshot(t, `["trade", "access", "egress-proxy"]`)
	// 把已写入的结果改成两把可检测的密钥。
	calls := 0
	deps := setupDeps{inspectExchangeKey: func(context.Context, *setupconfig.Snapshot, setupconfig.ExchangeKey) ([]byte, error) {
		calls++
		return nil, context.DeadlineExceeded
	}}
	applied := []setupclient.ExchangeKeyResult{
		{SecretID: "exchange-binance-production-main", State: setupclient.ExchangeKeyExists},
		{SecretID: "exchange-okx-testnet-revoked", State: setupclient.ExchangeKeyExists},
	}
	checks := inspectApplied(context.Background(), deps, snapshot, applied)
	require.Len(t, checks, 2)
	assert.Equal(t, 1, calls, "第一把连不上后不再逐把调用")
	assert.Equal(t, "pending", checks[0].Status)
	assert.Contains(t, checks[0].Message, "秒没有回应")
	assert.Equal(t, "pending", checks[1].Status)
	assert.Contains(t, checks[1].Message, "moox-cli trade inspect-credential")
}

// name 叫 readonly 却没写 readonly = true：Collector 按这个固定 ID 读取，要提醒。
func TestSetupApplyWarnsWhenReadonlyNamedKeyIsNotMarkedReadonly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moox.toml")
	body := strings.Replace(exchangeKeysManifestBody, "%s", `["trade", "access", "egress-proxy"]`, 1)
	body = strings.Replace(body, "api_key = \"recognizable-ro-key\"\nsecret_key = \"recognizable-ro-secret\"\nreadonly = true", "api_key = \"recognizable-ro-key\"\nsecret_key = \"recognizable-ro-secret\"", 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, dir)
	require.NoError(t, err)
	deps := setupDeps{inspectExchangeKey: func(context.Context, *setupconfig.Snapshot, setupconfig.ExchangeKey) ([]byte, error) {
		return []byte(inspectionPassed), nil
	}}
	checks := inspectApplied(context.Background(), deps, snapshot, appliedExchangeKeys())
	warned := ""
	for _, check := range checks {
		if check.SecretID == "exchange-binance-production-readonly" {
			require.NotEmpty(t, check.Warnings)
			warned = strings.Join(check.Warnings, "；")
		}
	}
	assert.Contains(t, warned, "readonly = true")
	assert.Contains(t, warned, "Collector")
	// 标了 readonly = true 的不提醒。
	checks = inspectApplied(context.Background(), deps, exchangeKeysSnapshot(t, `["trade", "access", "egress-proxy"]`), appliedExchangeKeys())
	for _, check := range checks {
		if check.SecretID == "exchange-binance-production-readonly" {
			assert.Empty(t, check.Warnings)
		}
	}
}
