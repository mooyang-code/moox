package exchange

import "testing"

func TestWalletFor(t *testing.T) {
	cases := []struct {
		name     string
		exchange Exchange
		mode     AccountMode
		market   MarketType
		want     Wallet
	}{
		{"币安普通账户现货", ExchangeBinance, AccountModeBinanceStandard, MarketTypeSpot, WalletSpot},
		{"币安普通账户合约", ExchangeBinance, AccountModeBinanceStandard, MarketTypeSwap, WalletFutures},
		{"币安统一账户现货", ExchangeBinance, AccountModeBinancePortfolioMargin, MarketTypeSpot, WalletMargin},
		{"币安统一账户合约", ExchangeBinance, AccountModeBinancePortfolioMargin, MarketTypeSwap, WalletUnified},
		{"OKX 现货", ExchangeOKX, AccountModeOKXUnified, MarketTypeSpot, WalletUnified},
		{"OKX 合约", ExchangeOKX, AccountModeOKXUnified, MarketTypeSwap, WalletUnified},
		{"尚未检测的币安账户按普通账户", ExchangeBinance, AccountModeUnspecified, MarketTypeSwap, WalletFutures},
		{"尚未检测的 OKX 账户按统一账户", ExchangeOKX, AccountModeUnspecified, MarketTypeSpot, WalletUnified},
	}
	for _, c := range cases {
		if got := WalletFor(c.exchange, c.mode, c.market); got != c.want {
			t.Errorf("%s：WalletFor = %s，期望 %s", c.name, got, c.want)
		}
	}
}
