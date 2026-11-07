package exchange

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
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

func TestInstrumentMinNotionalAt(t *testing.T) {
	price := shared.MustDecimal("50000")
	tests := []struct {
		name       string
		instrument Instrument
		want       string
	}{
		{"交易所给了最小名义金额就用它（币安）", Instrument{MinNotional: shared.MustDecimal("5"), MinExchangeQuantity: shared.MustDecimal("0.001"), ContractValue: shared.MustDecimal("1")}, "5"},
		{"OKX 现货：minSz × 价格", Instrument{MinExchangeQuantity: shared.MustDecimal("0.00001"), ContractValue: shared.MustDecimal("1")}, "0.5"},
		{"OKX 永续：minSz（张）× ctVal × 价格，ctVal 不能漏", Instrument{MinExchangeQuantity: shared.MustDecimal("1"), ContractValue: shared.MustDecimal("0.01")}, "500"},
		{"缺合约面值按 1", Instrument{MinExchangeQuantity: shared.MustDecimal("0.001")}, "50"},
		{"没有最小下单量没有下限", Instrument{ContractValue: shared.MustDecimal("0.01")}, "0"},
	}
	for _, test := range tests {
		if got := test.instrument.MinNotionalAt(price); got.String() != test.want {
			t.Errorf("%s: %s，想要 %s", test.name, got, test.want)
		}
	}
	if got := (Instrument{MinExchangeQuantity: shared.MustDecimal("1")}).MinNotionalAt(shared.Zero()); !got.IsZero() {
		t.Errorf("价格为 0 时没有下限：%s", got)
	}
}
