package marketwiring

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarketProviderSymbolForCryptoDerivesBinanceSymbols(t *testing.T) {
	for _, test := range []struct {
		name    string
		market  string
		subject string
		want    string
	}{
		{name: "spot", market: "spot", subject: "BTC-USDT", want: "BTCUSDT"},
		{name: "swap", market: "swap", subject: "1000BONK-USDT", want: "1000BONKUSDT"},
		{name: "legacy subject", market: "spot", subject: "BTC-USDT", want: "BTCUSDT"},
		{name: "chinese swap", market: "swap", subject: "币安人生-USDT", want: "币安人生USDT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveSymbol("binance", "crypto", test.market, test.subject)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
