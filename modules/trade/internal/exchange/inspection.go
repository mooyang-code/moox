package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// 检测结果里阻断项的编码。任一阻断项出现，账户都不能建立、已有账户停止下单。
const (
	// FindingWithdrawPermission：密钥带有提现权限。
	FindingWithdrawPermission = "withdraw_permission"
	// FindingSuspectedPortfolioMargin：papi、fapi 都返回鉴权类错误，且密钥开了合约权限，
	// 疑似密钥建于开通统一账户之前（开通后 fapi 不可用）。
	FindingSuspectedPortfolioMargin = "suspected_portfolio_margin"
	// FindingNoUniversalTransfer：普通账户同时开通现货和合约，但密钥没有万向划转权限，钱包之间无法划转资金。
	FindingNoUniversalTransfer = "no_universal_transfer"
	// FindingHedgeMode：合约是双向持仓，MooX 只支持单向。
	FindingHedgeMode = "hedge_position_mode"
	// FindingSpotModeWithSwap：OKX 现货模式账户（acctLv=1）却开通了 SWAP。
	FindingSpotModeWithSwap = "okx_spot_mode_with_swap"
	// FindingCredentialRejected：交易所以鉴权、权限类错误码拒绝了这把密钥（无效、IP 不在白名单、没有读取权限等）。
	FindingCredentialRejected = "credential_rejected"
	// FindingMarketNotPermitted：开通的市场，密钥没有对应的交易权限。
	FindingMarketNotPermitted = "market_not_permitted"
)

// PositionMode 是合约的持仓模式。
type PositionMode string

const (
	PositionModeUnknown PositionMode = ""
	PositionModeOneWay  PositionMode = "ONE_WAY"
	PositionModeHedge   PositionMode = "HEDGE"
)

// InspectionFinding 是检测发现的一条问题：阻断项或提醒。
type InspectionFinding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Inspection 是一次交易所密钥检测的结果。它记录检测到的事实；阻断项由事实和账户开通的市场推出（BlockersFor），
// 这样同一份事实可以用于“预览”（不知道要开哪些市场）和建账户后的复查。
type Inspection struct {
	Exchange    Exchange           `json:"exchange"`
	Environment AccountEnvironment `json:"environment"`
	AccountMode AccountMode        `json:"account_mode"`
	// Skipped 表示测试网跳过了权限与类型检测（币安测试网没有 sapi、papi）。
	Skipped bool `json:"skipped,omitempty"`

	// 权限。
	CanWithdraw       bool `json:"can_withdraw"`
	CanTradeSpot      bool `json:"can_trade_spot"`
	CanTradeSwap      bool `json:"can_trade_swap"`
	UniversalTransfer bool `json:"universal_transfer"`
	IPRestricted      bool `json:"ip_restricted"`
	// RawPermissions 是交易所原样返回的权限描述（OKX 的 perm、绑定 IP），只用于展示。
	RawPermissions string `json:"raw_permissions,omitempty"`

	// OKXAcctLv 是 OKX 账户层级：1 现货模式，2 合约模式，3 跨币种保证金，4 组合保证金。
	OKXAcctLv string `json:"okx_acct_lv,omitempty"`
	// PositionMode 只在能读到时记录；读不到（密钥没开合约）为空。
	PositionMode PositionMode `json:"position_mode,omitempty"`
	// MultiAssetsMargin 是币安普通账户合约钱包的多资产模式；MooX 允许，只作记录。
	MultiAssetsMargin bool `json:"multi_assets_margin,omitempty"`
	// PortfolioMarginSuspected 对应 FindingSuspectedPortfolioMargin。
	PortfolioMarginSuspected bool `json:"portfolio_margin_suspected,omitempty"`
	// CredentialRejected 记录鉴权类错误的说明；非空表示交易所拒绝了这把密钥。
	CredentialRejected string `json:"credential_rejected,omitempty"`

	// AvailableMarkets 是这把密钥当前能开通的市场。
	AvailableMarkets []MarketType `json:"available_markets,omitempty"`
	// Blockers 是按检测时的市场算出的阻断项，只用于展示；执行准入以 BlockersFor 重新计算的结果为准。
	Blockers []InspectionFinding `json:"blockers,omitempty"`
	Warnings []InspectionFinding `json:"warnings,omitempty"`

	InspectedAt time.Time `json:"inspected_at"`
	// CredentialFingerprint 是被检测的密钥内容（API Key）的哈希前缀，不存明文。
	// 同一个密钥 ID 下直接改了密钥内容时，指纹对不上：会话不会绑定没检测过的密钥，并立即重新检测。
	CredentialFingerprint string `json:"credential_fingerprint,omitempty"`

	// 复查状态：传输错误（超时、限频、5xx）不下结论，保留上次结果，连续失败到阈值再告警。
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastFailure         string    `json:"last_failure,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitzero"`
}

// BlockersFor 按账户开通的市场推出阻断项。markets 为空时按“这把密钥能开通的全部市场”推出，用于预览。
func (i Inspection) BlockersFor(markets []MarketType) []InspectionFinding {
	// 没有检测过（零值）和测试网跳过检测的，没有阻断项。
	if i.Skipped || i.InspectedAt.IsZero() {
		return nil
	}
	if len(markets) == 0 {
		markets = i.AvailableMarkets
	}
	var findings []InspectionFinding
	add := func(code, message string) {
		findings = append(findings, InspectionFinding{Code: code, Message: message})
	}
	if i.CredentialRejected != "" {
		add(FindingCredentialRejected, "交易所拒绝了这把密钥："+i.CredentialRejected)
		return findings
	}
	if i.CanWithdraw {
		add(FindingWithdrawPermission, "密钥带有提现权限，请在交易所关闭提现权限后重新录入")
	}
	if i.PortfolioMarginSuspected {
		add(FindingSuspectedPortfolioMargin, "疑似统一账户，且密钥建于开通统一账户之前：请在交易所重新创建 API 密钥后再录入")
	}
	hasSpot, hasSwap := slices.Contains(markets, MarketTypeSpot), slices.Contains(markets, MarketTypeSwap)
	if i.Exchange == ExchangeBinance && i.AccountMode == AccountModeBinanceStandard && hasSpot && hasSwap && !i.UniversalTransfer {
		add(FindingNoUniversalTransfer, "普通账户同时开通现货和合约，密钥需要“万向划转”权限才能在两个钱包之间划转资金")
	}
	if hasSwap && i.PositionMode == PositionModeHedge {
		add(FindingHedgeMode, "合约是双向持仓，MooX 只支持单向持仓，请在交易所切换后再录入")
	}
	if i.Exchange == ExchangeOKX && i.OKXAcctLv == "1" && hasSwap {
		add(FindingSpotModeWithSwap, "OKX 账户是现货模式（acctLv=1），不能做永续，请在交易所切换账户模式")
	}
	if hasSpot && !i.CanTradeSpot && !i.PortfolioMarginSuspected {
		add(FindingMarketNotPermitted, "密钥没有现货交易权限")
	}
	if hasSwap && !i.CanTradeSwap && !i.PortfolioMarginSuspected {
		add(FindingMarketNotPermitted, "密钥没有合约交易权限")
	}
	return findings
}

// CredentialFingerprint 返回 API Key 的指纹（SHA-256 前 12 个十六进制字符）；API Key 为空返回空串。
func CredentialFingerprint(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("moox-credential:" + apiKey))
	return hex.EncodeToString(sum[:])[:12]
}

// Blocked 报告按这些市场算有没有阻断项。
func (i Inspection) Blocked(markets []MarketType) bool {
	return len(i.BlockersFor(markets)) > 0
}

// ParseInspection 解析账户里存的检测结果；空串和 "{}" 返回零值（还没有检测过）。
func ParseInspection(raw string) (Inspection, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return Inspection{}, nil
	}
	var value Inspection
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return Inspection{}, err
	}
	return value, nil
}

// Encode 序列化成存进账户的 JSON。
func (i Inspection) Encode() (string, error) {
	data, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// InspectRequest 是一次检测的输入。
type InspectRequest struct {
	Environment AccountEnvironment
	Credential  Credential
	// Markets 是要开通的市场，空表示预览（按这把密钥能开通的全部市场判断）。
	Markets []MarketType
}

// Inspector 检测交易所密钥的账户类型和权限。返回错误表示这次没有检测成功（超时、限频、5xx 或无法确认的响应），
// 不代表账户有问题，调用方保留上次结果；鉴权、权限类错误码是确定的结论，记在 Inspection 里而不是返回错误。
type Inspector func(context.Context, InspectRequest) (Inspection, error)

// ErrInspectionInconclusive 标记检测没有得出结论。
var ErrInspectionInconclusive = errors.New("交易所密钥检测没有得出结论")
