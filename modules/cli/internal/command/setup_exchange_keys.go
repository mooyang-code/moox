package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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

// inspectCallTimeout 是经网关调一次检测的期限：要长于 Trade 自己的检测时限（12 秒，服务端超时 15 秒），
// 否则正常但耗时 10 多秒的检测会被说成“Trade 可能没部署”；又要短于网关默认的 30 秒，海外机连不上时每把密钥不能各等 30 秒。
var inspectCallTimeout = 20 * time.Second

// inspectCommandHint 是检测命令的写法，提示里直接给出可复制的完整命令。
func inspectCommandHint(key setupconfig.ExchangeKey) string {
	environment := "ACCOUNT_ENVIRONMENT_PRODUCTION"
	if key.Environment == "testnet" {
		environment = "ACCOUNT_ENVIRONMENT_TESTNET"
	}
	return fmt.Sprintf("moox-cli trade inspect-credential --exchange EXCHANGE_%s --environment %s --credential-secret-id %s",
		strings.ToUpper(key.Exchange), environment, key.ID())
}

// tradeInspectRequest 是 InspectExchangeCredential 的请求（JSON，枚举写名字）。markets 不传：阻断项只有和市场无关的，和市场有关的问题在提醒里说明。
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
	ctx, cancel := context.WithTimeout(ctx, inspectCallTimeout)
	defer cancel()
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
	// unreachable 为真表示前面一把密钥的检测调用就连不上 Trade：其余密钥不再逐把等超时，直接提示部署后再检测。
	unreachable := false
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
		case unreachable:
			check.Status = exchangeCheckPending
			check.Message = "前面一把密钥的检测已经连不上 Trade，这把不再逐个等待：确认 Trade 已部署并可访问后，执行 " + inspectCommandHint(key)
		default:
			raw, err := deps.inspectExchangeKey(ctx, snapshot, key)
			check = classifyInspection(result.SecretID, key, raw, err)
			unreachable = err != nil
		}
		if key.Name == "readonly" && !key.ReadOnly && known &&
			strings.EqualFold(key.Exchange, "binance") && strings.EqualFold(key.Environment, "production") {
			// Collector 检测币安现货下架读的正是 exchange-binance-production-readonly（只有币安生产环境这一把有固定用途）：
			// 名字叫 readonly 的密钥不标 readonly = true，
			// 会被当成可以建执行账户的密钥，也不会跳过检测；只读密钥要写 readonly = true，并且在交易所只开读取权限。
			check.Warnings = append(check.Warnings, "这把密钥的名称是 readonly（Collector 按固定 ID "+result.SecretID+" 读取它），但没有写 readonly = true：它会被当成可以建执行账户的密钥。只读密钥请写 readonly = true，并在交易所只开读取权限")
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
		reason := "它可能还没部署或暂时不可用"
		if errors.Is(callErr, context.DeadlineExceeded) {
			reason = fmt.Sprintf("等了 %d 秒没有回应：海外机上的 Trade 可能没有部署，或网络不通", int(inspectCallTimeout/time.Second))
		}
		check.Message = "连不上 Trade：" + reason + "。部署完成后在海外机上检测这把密钥，执行 " + inspectCommandHint(key) + "，或在管理台点“测试连接”"
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
		// Trade 把“检测没有完成”（交易所接口超时、限频）和其他未归类的内部错误（例如从 Admin 读密钥失败）都归到 INNER_ERR，
		// 这里分不出是哪一种，所以不说成“交易所接口不可用”，也不断定密钥有问题。
		check.Status = exchangeCheckIncomplete
		check.Message = "检测失败，原因未明确（常见原因是交易所接口超时或限频，也可能是 Trade 内部错误），不能断定密钥有问题；稍后重试，仍失败请查看 Trade 日志：" + inspectCommandHint(key)
		if detail := strings.TrimSpace(response.RetInfo.Msg); detail != "" {
			check.Message += "（Trade 返回：" + truncateText(detail, 160) + "）"
		}
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
		if len(check.Warnings) > 0 {
			// 这里不带市场，只判断了和市场无关的问题；和市场有关的（双向持仓、缺万向划转权限等）在提醒里，建账户时按所选市场再判断。
			check.Message = "检测通过（只判断了和市场无关的问题，和市场有关的见提醒）"
		}
	}
	return check
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
