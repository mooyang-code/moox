package shared

import "testing"

func TestDecimalExactArithmetic(t *testing.T) {
	a := MustDecimal("0.1").Add(MustDecimal("0.2"))
	if a.String() != "0.3" {
		t.Fatalf("sum=%s", a.String())
	}
	if MustDecimal("1.2300").String() != "1.23" {
		t.Fatal("not normalized")
	}
}

func TestDecimalRejectsUnsafeForms(t *testing.T) {
	for _, raw := range []string{"", "NaN", "Inf", "1e3", "+1", ".1", "01"} {
		if _, err := ParseDecimal(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if err := MustDecimal("-1").Validate(2, false); err == nil {
		t.Fatal("accepted negative")
	}
	if err := MustDecimal("1.001").Validate(2, true); err == nil {
		t.Fatal("accepted excessive scale")
	}
}

func TestDecimalIsInteger(t *testing.T) {
	if !MustDecimal("2.00").IsInteger() {
		t.Fatal("expected integer")
	}
	if MustDecimal("1").Div(MustDecimal("3")).IsInteger() {
		t.Fatal("accepted fractional rational")
	}
}
