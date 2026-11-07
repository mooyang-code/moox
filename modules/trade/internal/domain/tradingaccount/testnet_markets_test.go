package tradingaccount

import (
	"errors"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 测试网一个账户只能开一个市场：应用层先拦下并给中文错误，不只靠表上的 CHECK。
func TestTestnetAccountCannotOpenBothMarkets(t *testing.T) {
	account := validSwapAccount()
	account.Markets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	account.ExecutionMode = exchange.ExecutionModeLive
	account.Environment = exchange.AccountEnvironmentTestnet
	account.CredentialSecretID = "secret-1"
	account.Paper = nil

	err := account.Validate()
	if !errors.Is(err, ErrInvalidAccount) || !strings.Contains(err.Error(), "测试网一个账户只能开通一个市场") {
		t.Fatalf("err = %v，想要测试网只能开一个市场的中文错误", err)
	}

	account.Environment = exchange.AccountEnvironmentProduction
	if err := account.Validate(); err != nil {
		t.Fatalf("生产环境可以同时开通两个市场：%v", err)
	}
}
