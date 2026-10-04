package exchange

import "testing"

func TestCanonicalInstrumentIDIsExchangeIndependent(t *testing.T) {
	for _, market := range []MarketType{MarketTypeSpot, MarketTypeSwap} {
		got, err := CanonicalInstrumentID(" btc ", "usdt", market)
		if err != nil {
			t.Fatalf("CanonicalInstrumentID() error = %v", err)
		}
		want := "BTC-USDT-" + string(market)
		if got != want {
			t.Fatalf("CanonicalInstrumentID() = %q, want %q", got, want)
		}
	}
}

func TestCanonicalInstrumentIDRejectsIncompleteIdentity(t *testing.T) {
	if _, err := CanonicalInstrumentID("", "USDT", MarketTypeSpot); err == nil {
		t.Fatal("CanonicalInstrumentID() accepted empty base asset")
	}
	if _, err := CanonicalInstrumentID(
		"BTC",
		"USDT",
		MarketTypeUnspecified,
	); err == nil {
		t.Fatal("CanonicalInstrumentID() accepted unspecified market")
	}
}
