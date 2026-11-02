package exchange

import (
	"errors"
	"testing"
)

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

func TestNormalizeInstrumentID(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		market    MarketType
		want      string
		wantErr   bool
	}{
		{name: "现货无后缀", raw: "BTC-USDT", market: MarketTypeSpot, want: "BTC-USDT-SPOT"},
		{name: "合约无后缀", raw: "BTC-USDT", market: MarketTypeSwap, want: "BTC-USDT-SWAP"},
		{name: "同市场后缀", raw: "BTC-USDT-SWAP", market: MarketTypeSwap, want: "BTC-USDT-SWAP"},
		{name: "小写和空格", raw: " btc-usdt-swap ", market: MarketTypeSwap, want: "BTC-USDT-SWAP"},
		{name: "不同市场后缀", raw: "BTC-USDT-SPOT", market: MarketTypeSwap, wantErr: true},
		{name: "交易所原生代码", raw: "BTCUSDT", market: MarketTypeSwap, wantErr: true},
		{name: "空串", raw: "", market: MarketTypeSwap, wantErr: true},
		{name: "未知市场", raw: "BTC-USDT", market: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeInstrumentID(tc.raw, tc.market)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInstrumentID) {
					t.Fatalf("应返回 ErrInvalidInstrumentID，实际 %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
}
