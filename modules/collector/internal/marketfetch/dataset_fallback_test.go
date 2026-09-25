package marketfetch

import "testing"

func TestDefaultProviderSymbolRejectsUnsupportedCryptoFallback(t *testing.T) {
	tests := []struct {
		subject string
		market  string
		wantErr bool
	}{
		{subject: "AAOIB-USDT-SPOT", market: "spot", wantErr: false},
		{subject: "1000CAT-USDT-SWAP", market: "swap", wantErr: false},
		{subject: "BTC-USDT-SPOT", market: "swap", wantErr: true},
		{subject: "BTC-USDT", market: "spot", wantErr: false},
		{subject: "BTC-USDT", market: "swap", wantErr: false},
		{subject: "BTCUSDT", market: "spot", wantErr: true},
	}
	for _, tt := range tests {
		if _, err := DefaultProviderSymbol("crypto", tt.market, tt.subject); (err != nil) != tt.wantErr {
			t.Fatalf("DefaultProviderSymbol(%q, %q) error = %v, wantErr %v", tt.subject, tt.market, err, tt.wantErr)
		}
	}
}
