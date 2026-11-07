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
	// 一律按后缀判断：单市场账户也不例外，加密货币标的没有后缀就没有市场；股票代码只在开通了股票市场的账户上有效。
	if got := validSpotAccount().MarketForInstrument("BTCUSDT"); got != "" {
		t.Fatalf("没有后缀的加密货币标的市场 = %q，想要空", got)
	}
	if got := validSpotAccount().MarketForInstrument("BTC-USDT-SWAP"); got != exchange.MarketTypeSwap {
		t.Fatalf("后缀指向的市场 = %q，不能被账户只开通现货改写", got)
	}
	stock := validSpotAccount()
	stock.Markets = []exchange.MarketType{exchange.MarketTypeStock}
	if got := stock.MarketForInstrument("600519.SH"); got != exchange.MarketTypeStock {
		t.Fatalf("股票市场 = %q", got)
	}
}
