package builtin

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"testing"
)

func TestCatalogConstructsDeclaredBuiltins(t *testing.T) {
	catalog := Default("../../config/markets/stock_cn/calendar.yaml")
	for _, id := range []string{"binance", "okx", "ifeng"} {
		if _, err := catalog.Provider(marketdata.ProviderID(id)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"stock_cn", "stock_us", "crypto_binance", "crypto_okx"} {
		if _, err := catalog.Market(marketdata.MarketID(id)); err != nil {
			t.Fatal(err)
		}
	}
}
