package discovery

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/source"
)

func TestFilterByQuoteAsset(t *testing.T) {
	instruments := []source.Instrument{
		{Symbol: "APT-USDT", QuoteAsset: "USDT"},
		{Symbol: "AR-BTC", QuoteAsset: "BTC"},
	}
	got := FilterByQuoteAsset(instruments, "usdt")
	if len(got) != 1 || got[0].Symbol != "APT-USDT" {
		t.Fatalf("FilterByQuoteAsset() = %+v, want APT-USDT", got)
	}
}
