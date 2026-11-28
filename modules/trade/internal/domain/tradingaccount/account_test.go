package tradingaccount

import (
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestValidateMarketsAndTestnetRestriction(t *testing.T) {
	both := []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	if err := ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, "", both); err != nil {
		t.Fatalf("模拟盘现货加合约应合法：%v", err)
	}
	// 币安测试网一个账户只能开一个市场；生产环境和 OKX 测试网可以同时开两个。
	err := ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModeLive, exchange.AccountEnvironmentTestnet, "", both)
	if !errors.Is(err, ErrInvalidAccount) || !strings.Contains(err.Error(), "币安测试网一个账户只能开通一个市场") {
		t.Fatalf("err = %v，想要测试网只能开一个市场的中文错误", err)
	}
	if err := ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModeLive, exchange.AccountEnvironmentProduction, "", both); err != nil {
		t.Fatalf("生产环境可以同时开通两个市场：%v", err)
	}
	if err := ValidateMarkets(exchange.ExchangeOKX, exchange.ExecutionModeLive, exchange.AccountEnvironmentTestnet, "", both); err != nil {
		t.Fatalf("OKX 模拟盘可以同时开通两个市场：%v", err)
	}

	for name, err := range map[string]error{
		"顺序不规范":      ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, "", []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}),
		"重复":         ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, "", []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSwap}),
		"空":          ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, "", nil),
		"股票混合现货":     ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, "", []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeEquity}),
		"模拟盘不能有账户类型": ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModePaper, exchange.AccountEnvironmentPaper, exchange.AccountModeBinanceStandard, []exchange.MarketType{exchange.MarketTypeSpot}),
		"未知账户类型":     ValidateMarkets(exchange.ExchangeBinance, exchange.ExecutionModeLive, exchange.AccountEnvironmentProduction, "UNKNOWN", []exchange.MarketType{exchange.MarketTypeSpot}),
	} {
		if !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("%s: err = %v，想要 ErrInvalidAccount", name, err)
		}
	}
}

func TestValidatePaperParamsAndLeverageRules(t *testing.T) {
	if err := ValidatePaperParams(shared.MustDecimal("1000"), shared.Zero(), shared.Zero(), shared.Zero(), DefaultOrderRetentionDays); err != nil {
		t.Fatalf("合法模拟参数被拒：%v", err)
	}
	if !errors.Is(ValidatePaperParams(shared.Zero(), shared.Zero(), shared.Zero(), shared.Zero(), DefaultOrderRetentionDays), ErrInvalidAccount) {
		t.Fatal("零初始资金应被拒绝")
	}
	if !errors.Is(ValidatePaperParams(shared.MustDecimal("100"), shared.Zero(), shared.Zero(), shared.MustDecimal("10000"), DefaultOrderRetentionDays), ErrInvalidAccount) {
		t.Fatal("滑点达到 10000 应被拒绝")
	}
	if !errors.Is(ValidateLeverageRules(false, exchange.MarginModeCross, nil), ErrInvalidAccount) {
		t.Fatal("无永续市场不能设保证金模式")
	}
	if !errors.Is(ValidateLeverageRules(false, exchange.MarginModeUnspecified, map[string]string{"BTC-USDT": "5"}), ErrInvalidAccount) {
		t.Fatal("无永续市场不能设杠杆")
	}
	if !errors.Is(ValidateLeverageRules(true, exchange.MarginModeUnspecified, nil), ErrInvalidAccount) {
		t.Fatal("永续市场必须全仓")
	}
	if !errors.Is(ValidateLeverageRules(true, exchange.MarginModeCross, map[string]string{"BTC-USDT": "0"}), ErrInvalidAccount) {
		t.Fatal("非正杠杆应被拒绝")
	}
	if err := ValidateLeverageRules(true, exchange.MarginModeCross, map[string]string{"BTC-USDT": "5"}); err != nil {
		t.Fatalf("合法永续杠杆被拒：%v", err)
	}
}
