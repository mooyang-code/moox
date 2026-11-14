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

// 除不尽的值（名义金额 ÷ 3 倍杠杆）转成字符串后必须仍是能读回的十进制小数，否则写库会被拒。
func TestDecimalNonTerminatingStringIsParseable(t *testing.T) {
	for _, tc := range []struct{ num, den, want string }{
		{"100", "3", "33.333333333333333333"},
		{"-100", "3", "-33.333333333333333333"},
		{"1", "7", "0.142857142857142857"},
		{"10", "4", "2.5"},
	} {
		got := MustDecimal(tc.num).Div(MustDecimal(tc.den)).String()
		if got != tc.want {
			t.Fatalf("%s/%s = %q, want %q", tc.num, tc.den, got, tc.want)
		}
		if _, err := ParseDecimal(got); err != nil {
			t.Fatalf("%s/%s 输出 %q 不能读回: %v", tc.num, tc.den, got, err)
		}
	}
}
