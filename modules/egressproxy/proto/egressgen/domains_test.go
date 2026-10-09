package egresspb

import "testing"

func TestDomainListMatchesExactAndWildcard(t *testing.T) {
	list, err := ParseDomainList([]string{"*.binance.com", "Data-API.binance.vision"})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"fapi.binance.com": true, "a.b.binance.com": true, "data-api.binance.vision": true,
		"FAPI.Binance.com.": true, " api.binance.com ": true,
		"binance.com": false, "evilbinance.com": false, "binance.com.evil.io": false, "api.binance.vision": false,
		"fapi.binance.com:443": false, "": false,
	} {
		if got := list.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestParseDomainListRejectsInvalidEntries(t *testing.T) {
	for _, entries := range [][]string{
		{"*."}, {"1.2.3.4"}, {"https://fapi.binance.com"}, {"fapi.binance.com:443"}, {"*.*.binance.com"},
		{"localhost"}, {"api.binance.com", "API.binance.com"},
	} {
		if _, err := ParseDomainList(entries); err == nil {
			t.Errorf("ParseDomainList(%q) 应当报错", entries)
		}
	}
}

func TestEmptyDomainListAllowsNothing(t *testing.T) {
	list, err := ParseDomainList(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Empty() || list.Allows("api.binance.com") {
		t.Fatal("空白名单不应放行任何域名")
	}
	var zero DomainList
	if !zero.Empty() || zero.Allows("api.binance.com") {
		t.Fatal("零值是空白名单")
	}
}
