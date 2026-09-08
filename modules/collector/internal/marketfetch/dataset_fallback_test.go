package marketfetch

import "testing"

func TestFallbackCryptoSymbol(t *testing.T) {
	tests := []struct {
		subject string
		market  string
		want    string
	}{
		{subject: "AAOIB-USDT-SPOT", market: "spot", want: "AAOIBUSDT"},
		{subject: "1000CAT-USDT-SWAP", market: "swap", want: "1000CATUSDT"},
		{subject: "BTC-USDT-SPOT", market: "swap", want: ""},
		{subject: "BTC-USDT", market: "spot", want: ""},
	}
	for _, tt := range tests {
		if got := fallbackCryptoSymbol(tt.subject, tt.market); got != tt.want {
			t.Fatalf("fallbackCryptoSymbol(%q, %q) = %q, want %q", tt.subject, tt.market, got, tt.want)
		}
	}
}
