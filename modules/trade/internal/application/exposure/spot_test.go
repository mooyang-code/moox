package exposure

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestSpotDust(t *testing.T) {
	instrument := store.InstrumentRecord{ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", MinNotional: "5"}
	for _, test := range []struct {
		total, price string
		dust         bool
	}{
		{"0.0005", "100", true},  // 取整后为 0
		{"0.02", "100", true},    // 2 低于最小名义金额 5
		{"0.02", "0", false},     // 拿不到报价只比数量
		{"0.0009", "0", true},    // 拿不到报价也能按数量判零头
		{"0.06", "100", false},   // 6 不低于 5
		{"1.0004", "100", false}, // 取整后照常能卖
	} {
		got := SpotDust(shared.MustDecimal(test.total), shared.MustDecimal(test.price), instrument)
		if got != test.dust {
			t.Fatalf("SpotDust(%s, %s) = %v, want %v", test.total, test.price, got, test.dust)
		}
	}
}

func TestSpotInstrumentsUsesSettlementQuotedTradableInstrument(t *testing.T) {
	instruments := []store.InstrumentRecord{
		{InstrumentID: "BTC-BUSD", BaseAsset: "BTC", QuoteAsset: "BUSD", Status: "TRADING"},
		{InstrumentID: "BTC-USDT-OFF", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "BREAK"},
		{InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "TRADING"},
	}
	got := SpotInstruments(instruments, "USDT")
	if got["BTC"].InstrumentID != "BTC-USDT" || len(got) != 1 {
		t.Fatalf("instruments = %+v", got)
	}
}
