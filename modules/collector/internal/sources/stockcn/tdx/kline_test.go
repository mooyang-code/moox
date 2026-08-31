package tdx

import "testing"

func TestParseSymbolRequiresExchange(t *testing.T) {
	if _, code, err := parseSymbol("SH.600000"); err != nil || code != "600000" {
		t.Fatalf("parseSymbol() = %q, %v", code, err)
	}
	if _, _, err := parseSymbol("600000"); err == nil {
		t.Fatal("bare TDX symbol should be rejected")
	}
}

func TestCategoryMapsCanonicalFrequencies(t *testing.T) {
	for value, want := range map[string]uint16{"1m": 7, "5m": 0, "1d": 4, "1M": 6} {
		got, err := category(value)
		if err != nil || uint16(got) != want {
			t.Fatalf("category(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
}
