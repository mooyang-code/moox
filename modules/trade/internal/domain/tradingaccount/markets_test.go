package tradingaccount

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestAccountMarketsMustBeCanonicalAndAccountModeOnlyForLiveCrypto(t *testing.T) {
	validate := func(mutate func(*Account)) error {
		account := validSwapAccount()
		mutate(&account)
		return account.Validate()
	}
	if err := validate(func(a *Account) {
		a.Markets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	}); err != nil {
		t.Fatalf("现货加合约应合法：%v", err)
	}
	for name, mutate := range map[string]func(*Account){
		"顺序不规范": func(a *Account) {
			a.Markets = []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSpot}
		},
		"重复": func(a *Account) {
			a.Markets = []exchange.MarketType{exchange.MarketTypeSwap, exchange.MarketTypeSwap}
		},
		"空": func(a *Account) { a.Markets = nil },
		"股票混合现货": func(a *Account) {
			a.Markets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeStock}
		},
		"模拟盘不能有账户类型": func(a *Account) { a.AccountMode = exchange.AccountModeBinanceStandard },
		"未知账户类型":     func(a *Account) { a.AccountMode = "UNKNOWN" },
	} {
		if err := validate(mutate); !errors.Is(err, ErrInvalidAccount) {
			t.Errorf("%s: err = %v，想要 ErrInvalidAccount", name, err)
		}
	}
}

func TestMarketForInstrument(t *testing.T) {
	both := validSwapAccount()
	both.Markets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	if got := both.MarketForInstrument("BTC-USDT-SPOT"); got != exchange.MarketTypeSpot {
		t.Fatalf("market = %q", got)
	}
	if got := both.MarketForInstrument("BTC-USDT-SWAP"); got != exchange.MarketTypeSwap {
		t.Fatalf("market = %q", got)
	}
	if !both.SupportsMarket(exchange.MarketTypeSpot) || both.SupportsMarket(exchange.MarketTypeStock) || !both.HasLeverageMarket() {
		t.Fatal("市场判断不对")
	}
	// 只开通一个市场时，标的 ID 不必带后缀。
	if got := validSpotAccount().MarketForInstrument("BTCUSDT"); got != exchange.MarketTypeSpot {
		t.Fatalf("market = %q", got)
	}
}
