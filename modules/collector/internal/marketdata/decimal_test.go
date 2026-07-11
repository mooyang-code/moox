package marketdata

import "testing"

func TestDecimalCanonicalValues(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"0", "0"},
		{"0.10", "0.1"},
		{"12.3400", "12.34"},
		{"-1.250", "-1.25"},
	} {
		d, err := ParseDecimal(tc.input)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", tc.input, err)
		}
		if got := d.String(); got != tc.want {
			t.Errorf("ParseDecimal(%q).String() = %q, want %q", tc.input, got, tc.want)
		}
	}
	left, _ := ParseDecimal("0.10")
	right, _ := ParseDecimal("0.1")
	if left.Cmp(right) != 0 {
		t.Fatal("equivalent decimals must compare equal")
	}
}

func TestDecimalRejectsInvalidSyntax(t *testing.T) {
	for _, input := range []string{"", ".1", "1.", "+1", "1e3", "NaN", "Infinity", "-Infinity", " 1"} {
		if _, err := ParseDecimal(input); err == nil {
			t.Errorf("ParseDecimal(%q) unexpectedly succeeded", input)
		}
	}
}

func TestDecimalValidation(t *testing.T) {
	negative, _ := ParseDecimal("-1")
	if err := negative.Validate(2, false); err == nil {
		t.Fatal("negative decimal should be rejected when negatives are disabled")
	}
	if err := negative.Validate(2, true); err != nil {
		t.Fatalf("negative decimal with allowNegative: %v", err)
	}
	tooPrecise, _ := ParseDecimal("1.001")
	if err := tooPrecise.Validate(2, true); err == nil {
		t.Fatal("decimal beyond scale limit should be rejected")
	}
	var zero Decimal
	if zero.String() != "0" || zero.Cmp(Decimal{}) != 0 || zero.IsNegative() {
		t.Fatal("zero value must behave as numeric zero")
	}
}
