package main

import "testing"

func TestBinanceSeriesTag(t *testing.T) {
	tests := []struct {
		marketType string
		want       string
	}{
		{marketType: "spot", want: "venue:binance"},
		{marketType: "swap", want: "venue:binance|market:swap"},
	}
	for _, test := range tests {
		t.Run(test.marketType, func(t *testing.T) {
			if got := binanceSeriesTag(test.marketType); got != test.want {
				t.Fatalf("binanceSeriesTag(%q) = %q, want %q", test.marketType, got, test.want)
			}
		})
	}
}
