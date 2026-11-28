package tradingaccount

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

var (
	ErrInvalidAccount       = errors.New("执行账户的参数不合法")
	ErrAccountNotExecutable = errors.New("执行账户当前不能下单")
)

const (
	// DefaultOrderRetentionDays 是模拟账户已结束订单与成交默认保留的天数（创建时没给就取它）。
	DefaultOrderRetentionDays = 5
	// MaxOrderRetentionDays 是保留天数的上限；0 表示不清理。
	MaxOrderRetentionDays = 365
)

// ValidOrderRetentionDays 报告保留天数是否在 0～365 之间。
func ValidOrderRetentionDays(days int) bool {
	return days >= 0 && days <= MaxOrderRetentionDays
}

// ValidStatus 报告执行账户状态是否合法。
func ValidStatus(status exchange.AccountStatus) bool {
	return status == exchange.AccountStatusEnabled ||
		status == exchange.AccountStatusDisabled ||
		status == exchange.AccountStatusError
}

// ValidateMarkets 校验已开通市场的规范写法、测试网单市场限制以及账户类型只用于加密货币实盘账户。
func ValidateMarkets(
	exch exchange.Exchange,
	execMode exchange.ExecutionMode,
	env exchange.AccountEnvironment,
	accountMode exchange.AccountMode,
	markets []exchange.MarketType,
) error {
	normalized, err := exchange.NormalizeMarkets(markets)
	if err != nil {
		return InvalidAccount(err.Error())
	}
	if !slices.Equal(normalized, markets) {
		return InvalidAccount("已开通的市场必须去重并按 spot、swap、equity 的顺序排列")
	}
	// 只有币安测试网要求一个账户一个市场；OKX 模拟盘一把密钥同时能做现货和永续。
	if env == exchange.AccountEnvironmentTestnet && exch == exchange.ExchangeBinance && len(markets) > 1 {
		return InvalidAccount("币安测试网一个账户只能开通一个市场：现货测试网和合约测试网是两套密钥")
	}
	if !accountMode.Valid() {
		return InvalidAccount("不支持的账户类型")
	}
	if accountMode != exchange.AccountModeUnspecified &&
		(execMode != exchange.ExecutionModeLive || slices.Contains(markets, exchange.MarketTypeEquity)) {
		return InvalidAccount("账户类型只用于加密货币实盘账户")
	}
	return nil
}

// ValidatePaperParams 校验模拟盘的初始资金、费率、滑点与订单保留天数。
func ValidatePaperParams(initialBalance, makerFeeRate, takerFeeRate, slippageBPS shared.Decimal, orderRetentionDays int) error {
	if initialBalance.Cmp(shared.Zero()) <= 0 ||
		makerFeeRate.Cmp(shared.Zero()) < 0 ||
		takerFeeRate.Cmp(shared.Zero()) < 0 ||
		slippageBPS.Cmp(shared.Zero()) < 0 ||
		slippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 ||
		!ValidOrderRetentionDays(orderRetentionDays) {
		return InvalidAccount("模拟配置不合法：初始资金要大于 0，费率不能为负，滑点在 0～10000 基点之间，订单保留天数在允许范围内")
	}
	return nil
}

// ValidateLeverageRules 校验保证金模式与杠杆设置是否与已开通市场一致。
func ValidateLeverageRules(hasLeverage bool, marginMode exchange.MarginMode, leverages map[string]string) error {
	if !hasLeverage {
		if marginMode != exchange.MarginModeUnspecified || len(leverages) != 0 {
			return InvalidAccount("没有开通 SWAP 的账户不能配置保证金模式或杠杆")
		}
		return nil
	}
	if marginMode != exchange.MarginModeCross {
		return InvalidAccount("SWAP 一律全仓，保证金模式必须是 CROSS")
	}
	for symbol, raw := range leverages {
		value, err := shared.ParseDecimal(raw)
		if strings.TrimSpace(symbol) == "" || err != nil || value.Cmp(shared.Zero()) <= 0 {
			return InvalidAccount("SWAP 杠杆必须为正数且绑定标的")
		}
	}
	return nil
}

// SharesFundsAcrossMarkets 报告各市场是否共用一个资金池：币安统一账户、OKX 统一账户和模拟盘都是，
// 现货买入和合约保证金花同一份结算币；币安普通账户的现货、合约钱包各用各的。
// 余额里的钱包标签（统一账户现货在 MARGIN、合约在 UNIFIED）只用于展示和对账，不决定资金池。
func SharesFundsAcrossMarkets(execMode exchange.ExecutionMode, exch exchange.Exchange, accountMode exchange.AccountMode) bool {
	if execMode == exchange.ExecutionModePaper {
		return true
	}
	mode := accountMode
	if mode == exchange.AccountModeUnspecified && exch == exchange.ExchangeOKX {
		mode = exchange.AccountModeOKXUnified
	}
	return mode == exchange.AccountModeOKXUnified || mode == exchange.AccountModeBinancePortfolioMargin
}

// MarketDataEnvironment 返回拉取公共行情与标的规则时使用的环境：模拟盘用生产环境行情，实盘用自身环境。
func MarketDataEnvironment(execMode exchange.ExecutionMode, env exchange.AccountEnvironment) exchange.AccountEnvironment {
	if execMode == exchange.ExecutionModePaper {
		return exchange.AccountEnvironmentProduction
	}
	return env
}

// InvalidAccount 构造带 ErrInvalidAccount 的参数错误。
func InvalidAccount(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidAccount, reason)
}
