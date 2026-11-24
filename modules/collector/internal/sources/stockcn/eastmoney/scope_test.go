package eastmoney

import "testing"

// InAShareScope 与 eastMoneyAShareFilter 一致：沪深主板、创业板在内，科创板与北交所不在。
func TestInAShareScope(t *testing.T) {
	cases := map[string]bool{
		"000001.XSHE": true, "300750.XSHE": true, "302132.XSHE": true,
		"600519.XSHG": true, "605599.XSHG": true,
		"688981.XSHG": false, "689009.XSHG": false,
		"920000.XBSE": false, "600519": false,
	}
	for id, want := range cases {
		if got := InAShareScope(id); got != want {
			t.Errorf("InAShareScope(%q) = %v, want %v", id, got, want)
		}
	}
}
