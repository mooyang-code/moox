package shared

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestFloorToStep(t *testing.T) {
	for _, c := range []struct{ value, step, want string }{
		{"10.37", "0.1", "10.3"}, {"10.3", "0.1", "10.3"}, {"0.0005", "0.001", "0"}, {"7", "2", "6"},
		{"100", "0.001", "100"}, {"-1", "0.1", "0"}, {"1", "0", "0"},
	} {
		require.Equal(t, c.want, FloorToStep(MustDecimal(c.value), MustDecimal(c.step)).String(), "%s / %s", c.value, c.step)
	}
	// 重复小数也不会丢精度：100 ÷ 3 向下取整到 0.01 是 33.33。
	require.Equal(t, "33.33", FloorToStep(MustDecimal("100").Div(MustDecimal("3")), MustDecimal("0.01")).String())
}
