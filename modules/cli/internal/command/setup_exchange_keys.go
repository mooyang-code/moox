package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// 交易所密钥（[[exchange_keys]]）写进 SecretMgr 之后的检测。
// 检测只有 Trade 里一份实现（InspectExchangeCredential）：CLI 经网关调它，不在本机重复实现，也不向交易所发请求。
// 检测结果不影响 setup apply 的退出码：密钥已经写入，检测只是告诉用户它能不能用来建执行账户。

// 检测结论。
const (
	exchangeCheckOK         = "ok"         // 检测通过，没有阻断项
	exchangeCheckBlocked    = "blocked"    // 检测完成，但有阻断项，建不了执行账户
	exchangeCheckSkipped    = "skipped"    // 不检测：只读密钥，或已在管理台删除
	exchangeCheckIncomplete = "incomplete" // 检测没有完成（交易所接口超时、限频等），不代表密钥有问题
	exchangeCheckFailed     = "failed"     // 密钥不可用（读不到、已停用等）
	exchangeCheckPending    = "pending"    // Trade 还没部署或连不上，部署后再检测
)

// exchangeKeyCheck 是一把交易所密钥的检测结果，不含任何密钥值。
type exchangeKeyCheck struct {
	SecretID         string   `json:"secret_id"`
	Status           string   `json:"status"`
	Message          string   `json:"message"`
	AccountMode      string   `json:"account_mode,omitempty"`
	AvailableMarkets []string `json:"available_markets,omitempty"`
	Blockers         []string `json:"blockers,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// inspectCommandHint 是检测命令的写法，提示里直接给出可复制的完整命令。
func inspectCommandHint(key setupconfig.ExchangeKey) string {
	environment := "ACCOUNT_ENVIRONMENT_PRODUCTION"
	if key.Environment == "testnet" {
		environment = "ACCOUNT_ENVIRONMENT_TESTNET"
	}
	return fmt.Sprintf("moox-cli trade inspect-credential --exchange EXCHANGE_%s --environment %s --credential-secret-id %s",
		strings.ToUpper(key.Exchange), environment, key.ID())
}

// tradeInspectRequest 是 InspectExchangeCredential 的请求（JSON，枚举写名字）。markets 不传：按这把密钥能开通的全部市场判断。
func tradeInspectRequest(key setupconfig.ExchangeKey) map[string]any {
	environment := "ACCOUNT_ENVIRONMENT_PRODUCTION"
	if key.Environment == "testnet" {
		environment = "ACCOUNT_ENVIRONMENT_TESTNET"
	}
	return map[string]any{
		"exchange": "EXCHANGE_" + strings.ToUpper(key.Exchange), "environment": environment, "credential_secret_id": key.ID(),
	}
}

// defaultInspectExchangeKey 经网关调 Trade 的 InspectExchangeCredential，返回原始响应。
// 调用本身失败（Trade 没部署、网关连不上）返回错误；业务失败在响应的 ret_info 里，由 classifyInspection 判断。
func defaultInspectExchangeKey(ctx context.Context, snapshot *setupconfig.Snapshot, key setupconfig.ExchangeKey) ([]byte, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	var response json.RawMessage
	client := &adminclient.Client{Gateway: gateway}
	if err := client.CallGatewayJSON(ctx, tradeConsole, "InspectExchangeCredential", tradeInspectRequest(key), &response); err != nil {
		return nil, err
	}
	return response, nil
}

// inspectApplied 逐把检测刚写入的交易所密钥。
// 不检测：readonly 的、已在管理台删除的、没写进 SecretMgr 的。Trade 没有部署（moox.toml 里没有 trade 组件）时只提示部署后怎么检测。
func inspectApplied(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, applied []setupclient.ExchangeKeyResult) []exchangeKeyCheck {
	keys := map[string]setupconfig.ExchangeKey{}
	for _, key := range snapshot.Manifest.ExchangeKeys {
		keys[key.ID()] = key
	}
	tradeConfigured := snapshot.Manifest.ComponentHost("trade") != ""
	var checks []exchangeKeyCheck
	for _, result := range applied {
		key, known := keys[result.SecretID]
		check := exchangeKeyCheck{SecretID: result.SecretID}
		switch {
		case !known:
			check.Status, check.Message = exchangeCheckSkipped, "moox.toml 里没有这把密钥"
		case result.ReadOnly || key.ReadOnly:
			check.Status, check.Message = exchangeCheckSkipped, "只读密钥，不做交易账户检测"
		case result.State == setupclient.ExchangeKeyDeleted:
			check.Status, check.Message = exchangeCheckSkipped, "已在管理台删除，未重建，不检测"
		case result.State == setupclient.ExchangeKeyMissing:
			check.Status, check.Message = exchangeCheckSkipped, "密钥管理里还没有这把密钥，不检测"
		case !tradeConfigured:
			check.Status = exchangeCheckPending
			check.Message = "Trade 还没部署：部署完成后在海外机上检测这把密钥，执行 " + inspectCommandHint(key) + "，或在管理台「设置 › 秘钥管理」点“测试连接”"
		default:
			raw, err := deps.inspectExchangeKey(ctx, snapshot, key)
			check = classifyInspection(result.SecretID, key, raw, err)
		}
		checks = append(checks, check)
	}
	return checks
}

// classifyInspection 按返回码分类，不匹配中文消息。注意阻断项出现在成功的响应里。
func classifyInspection(secretID string, key setupconfig.ExchangeKey, raw []byte, callErr error) exchangeKeyCheck {
	check := exchangeKeyCheck{SecretID: secretID}
	if callErr != nil {
		check.Status = exchangeCheckPending
		check.Message = "连不上 Trade：它可能还没部署或暂时不可用。部署完成后在海外机上检测这把密钥，执行 " + inspectCommandHint(key) + "，或在管理台点“测试连接”"
		return check
	}
	var response struct {
		RetInfo struct {
			Code json.RawMessage `json:"code"`
			Msg  string          `json:"msg"`
		} `json:"ret_info"`
		Inspection struct {
			AccountMode      string   `json:"account_mode"`
			Skipped          bool     `json:"skipped"`
			AvailableMarkets []string `json:"available_markets"`
			Blockers         []struct {
				Message string `json:"message"`
			} `json:"blockers"`
			Warnings []struct {
				Message string `json:"message"`
			} `json:"warnings"`
		} `json:"inspection"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		check.Status, check.Message = exchangeCheckIncomplete, "Trade 的返回看不懂，稍后重试："+inspectCommandHint(key)
		return check
	}
	switch strings.Trim(strings.TrimSpace(string(response.RetInfo.Code)), `"`) {
	case "", "0", "null", "SUCCESS":
	case "4", "INNER_ERR":
		// 检测没有完成：交易所接口超时、限频等，Trade 不下结论。不能当作密钥有问题。
		check.Status = exchangeCheckIncomplete
		check.Message = "检测没有完成（交易所接口暂时不可用或限频），不代表密钥有问题；稍后重试：" + inspectCommandHint(key)
		return check
	default:
		check.Status = exchangeCheckFailed
		check.Message = "密钥不可用：" + response.RetInfo.Msg
		return check
	}
	inspection := response.Inspection
	check.AccountMode, check.AvailableMarkets = inspection.AccountMode, inspection.AvailableMarkets
	for _, item := range inspection.Blockers {
		check.Blockers = append(check.Blockers, item.Message)
	}
	for _, item := range inspection.Warnings {
		check.Warnings = append(check.Warnings, item.Message)
	}
	switch {
	case len(check.Blockers) > 0:
		check.Status, check.Message = exchangeCheckBlocked, "检测完成，但有阻断项，这把密钥现在建不了执行账户"
	case inspection.Skipped:
		check.Status, check.Message = exchangeCheckOK, "测试网跳过了权限与账户类型检测"
	default:
		check.Status, check.Message = exchangeCheckOK, "检测通过"
	}
	return check
}
