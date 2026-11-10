package exchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	// FindingClockSkew：本机时钟与交易所相差超过 ClockSkewBlock，下单会被交易所拒绝或卡在结果未知。
	FindingClockSkew = "clock_skew"
)

// 本机时钟与交易所时间偏差的阈值：超过 ClockSkewWarn 提示，达到 ClockSkewBlock 阻断。
const (
	ClockSkewWarn  = time.Second
	ClockSkewBlock = 3 * time.Second
)

// MeasureClockSkew 用一次取交易所时间的请求估算本机时钟偏差（交易所时间减本机时间）：
// 本机时间取请求发出和收到响应的中点，抵消往返延迟。往返超过 ClockSkewWarn 的样本误差太大，按没测到处理（ok 为 false）。
func MeasureClockSkew(sentAt, receivedAt, serverTime time.Time) (skew time.Duration, ok bool) {
	roundTrip := receivedAt.Sub(sentAt)
	if roundTrip > ClockSkewWarn {
		return 0, false
	}
	return serverTime.Sub(sentAt.Add(roundTrip / 2)), true
}

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
	// ClockSkewMillis 是本机时钟相对交易所的偏差（毫秒，交易所时间减本机时间）；没取到交易所时间时为 0。
	ClockSkewMillis int64 `json:"clock_skew_ms,omitempty"`

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

// clockSkew 返回本机时钟偏差的绝对值。
func (i Inspection) clockSkew() time.Duration {
	skew := time.Duration(i.ClockSkewMillis) * time.Millisecond
	if skew < 0 {
		return -skew
	}
	return skew
}

// BlockersFor 按账户开通的市场推出阻断项。markets 为空（预览时还没选市场）只给和市场无关的阻断项：
// 密钥被拒绝、带提现权限、疑似统一账户旧密钥；和市场有关的（万向划转、双向持仓、OKX 现货模式、市场交易权限）
// 要等选定市场后再判断，否则只想开现货的账户会被合约才有的问题挡住。
func (i Inspection) BlockersFor(markets []MarketType) []InspectionFinding {
	// 没有检测过（零值）和测试网跳过检测的，没有阻断项。
	if i.Skipped || i.InspectedAt.IsZero() {
		return nil
	}
	var findings []InspectionFinding
	add := func(code, message string) {
		findings = append(findings, InspectionFinding{Code: code, Message: message})
	}
	if i.CredentialRejected != "" {
		add(FindingCredentialRejected, "交易所拒绝了这把密钥："+i.CredentialRejected)
		return findings
	}
	if skew := i.clockSkew(); skew >= ClockSkewBlock {
		add(FindingClockSkew, "本机时钟与交易所相差过大，下单会被拒绝或卡在结果未知：请检查本机 NTP 校时后重新检测")
	}
	// 没有读到账户类型（签名检测没有结论，只测到了时钟）：账户相关的事实都不知道，只能就时钟下结论。
	if i.AccountMode == AccountModeUnspecified {
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

// ApplyFindings 按要开通的市场填写阻断项和提醒。markets 为空（预览）时阻断项只有和市场无关的，
// 和市场有关的问题（只在开通合约或同时开通现货、合约时才成立的）写成提醒，说明开通对应市场时会被阻断。
func (i *Inspection) ApplyFindings(markets []MarketType) {
	i.Blockers = i.BlockersFor(markets)
	i.Warnings = nil
	if skew := i.clockSkew(); !i.Skipped && skew > ClockSkewWarn && skew < ClockSkewBlock {
		i.Warnings = append(i.Warnings, InspectionFinding{
			Code:    FindingClockSkew,
			Message: fmt.Sprintf("本机时钟与交易所相差 %.1f 秒，建议检查本机 NTP 校时（达到 %d 秒会阻断下单）", skew.Seconds(), int(ClockSkewBlock.Seconds())),
		})
	}
	if len(markets) != 0 {
		return
	}
	base := make(map[string]struct{}, len(i.Blockers))
	for _, finding := range i.Blockers {
		base[finding.Message] = struct{}{}
	}
	// 按现货和永续都开通来算（不是按“这把密钥现在能开通的市场”：OKX 现货模式、没有合约交易权限时 SWAP 不在可开通市场里，
	// 但正是这种密钥需要提醒“开通永续时会被阻断”）。
	for _, finding := range i.BlockersFor([]MarketType{MarketTypeSpot, MarketTypeSwap}) {
		if _, ok := base[finding.Message]; ok {
			continue
		}
		i.Warnings = append(i.Warnings, InspectionFinding{Code: finding.Code, Message: "开通对应市场时会被阻断：" + finding.Message})
	}
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
	// Markets 是要开通的市场，空表示预览（阻断项只有和市场无关的，和市场有关的问题写成提醒）。
	Markets []MarketType
}

// Inspector 检测交易所密钥的账户类型和权限。返回错误表示这次没有检测成功（超时、限频、5xx 或无法确认的响应），
// 不代表账户有问题，调用方保留上次结果；鉴权、权限类错误码是确定的结论，记在 Inspection 里而不是返回错误。
type Inspector func(context.Context, InspectRequest) (Inspection, error)

// ErrInspectionInconclusive 标记检测没有得出结论。
var ErrInspectionInconclusive = errors.New("交易所密钥检测没有得出结论")
