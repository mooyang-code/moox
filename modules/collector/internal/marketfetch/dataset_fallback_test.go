package marketfetch

import "testing"

func TestDefaultProviderSymbolAcceptsChineseCryptoAssets(t *testing.T) {
	got, err := DefaultProviderSymbol("crypto", "spot", "币安人生-USDT")
	if err != nil {
		t.Fatalf("DefaultProviderSymbol(币安人生-USDT) error = %v", err)
	}
	if got != "币安人生USDT" {
		t.Fatalf("DefaultProviderSymbol(币安人生-USDT) = %q, want 币安人生USDT", got)
	}
}

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
		{subject: "币安人生-USDT", market: "spot", wantErr: false},
		{subject: "牛来-USDT", market: "swap", wantErr: false},
		{subject: "币安 人生-USDT", market: "spot", wantErr: true},
		{subject: "BTCUSDT", market: "spot", wantErr: true},
	}
	for _, tt := range tests {
		if _, err := DefaultProviderSymbol("crypto", tt.market, tt.subject); (err != nil) != tt.wantErr {
			t.Fatalf("DefaultProviderSymbol(%q, %q) error = %v, wantErr %v", tt.subject, tt.market, err, tt.wantErr)
		}
	}
}
