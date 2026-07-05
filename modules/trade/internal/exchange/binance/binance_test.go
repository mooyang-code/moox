package binance

import "testing"

func TestBinancePositionSide(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "long", in: "long", want: "LONG"},
		{name: "short", in: "SHORT", want: "SHORT"},
		{name: "both", in: "both", want: "BOTH"},
		{name: "net", in: "net", want: "BOTH"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := binancePositionSide(tt.in); got != tt.want {
				t.Fatalf("binancePositionSide(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBinanceShouldSendReduceOnly(t *testing.T) {
	tests := []struct {
		name       string
		reduceOnly bool
		posSide    string
		want       bool
	}{
		{name: "spot or one way close", reduceOnly: true, posSide: "", want: true},
		{name: "one way explicit both", reduceOnly: true, posSide: "BOTH", want: true},
		{name: "hedge long omits reduceOnly", reduceOnly: true, posSide: "LONG", want: false},
		{name: "hedge short omits reduceOnly", reduceOnly: true, posSide: "SHORT", want: false},
		{name: "not reduce only", reduceOnly: false, posSide: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := binanceShouldSendReduceOnly(tt.reduceOnly, tt.posSide); got != tt.want {
				t.Fatalf("binanceShouldSendReduceOnly(%v, %q) = %v, want %v", tt.reduceOnly, tt.posSide, got, tt.want)
			}
		})
	}
}
