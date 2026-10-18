package quant

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDecimalCanonicalArithmetic(t *testing.T) {
	a := Must("1.25")
	b := Must("0.75")
	require.Equal(t, "2", a.Add(b).String())
	require.Equal(t, "0.5", a.Sub(b).String())
	require.Equal(t, "-1.25", a.Neg().String())
	for _, raw := range []string{"1e3", "+1", ".5", "1.", "01", "NaN", "Inf", "1.1234567890123456789"} {
		_, err := Parse(raw)
		require.Error(t, err, raw)
	}
}

// DSL 数字字面量限制长度、指数与量级：1e999999 这类写法不能展开成巨大的整数拖垮进程。
func TestParseLiteralBoundsMagnitude(t *testing.T) {
	for _, raw := range []string{".5", "+0.5", "5e-1", "999999999", "1e8"} {
		if _, err := ParseLiteral(raw); err != nil {
			t.Fatalf("%s 应可解析：%v", raw, err)
		}
	}
	started := time.Now()
	for _, raw := range []string{"1e999999", "1e31", "1e9", "-1000000000", strings.Repeat("9", 65)} {
		if _, err := ParseLiteral(raw); !errors.Is(err, ErrInvalidDecimal) || len(err.Error()) > 200 {
			t.Fatalf("%s 应以简短的错误拒绝：%v", raw, err)
		}
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("拒绝超大字面量不应展开计算：耗时 %s", elapsed)
	}
}

// DivideProportional 的各份之和恰好等于 total：剩下的最小单位补给靠前的键，不因逐份截断而少掉零头。
func TestDivideProportionalSumsExactly(t *testing.T) {
	keys := []string{"a", "b", "c"}
	shares := DivideProportional(Must("1"), keys, []int64{3, 2, 1})
	sum := Zero()
	for _, key := range keys {
		sum = sum.Add(shares[key])
	}
	if sum.Cmp(Must("1")) != 0 {
		t.Fatalf("各份之和应恰好为 1：%s（%v）", sum.String(), shares)
	}
	if shares["a"].Cmp(shares["b"]) <= 0 || shares["b"].Cmp(shares["c"]) <= 0 {
		t.Fatalf("份数越大分得越多：%v", shares)
	}
	// 200 份时之和仍然精确。
	many := make([]string, 200)
	parts := make([]int64, 200)
	for i := range many {
		many[i], parts[i] = fmt.Sprintf("k%03d", i), int64(200-i)
	}
	total := Zero()
	for _, share := range DivideProportional(Must("0.9"), many, parts) {
		total = total.Add(share)
	}
	if total.Cmp(Must("0.9")) != 0 {
		t.Fatalf("200 份之和应恰好为 0.9：%s", total.String())
	}
}

func TestRatRoundTrip(t *testing.T) {
	for _, raw := range []string{"0", "0.4", "-0.333333333333333333", "123.000000000000000001"} {
		if got := FromRat(Must(raw).Rat()).String(); got != Must(raw).String() {
			t.Errorf("%s 经有理数往返后变成 %s", raw, got)
		}
	}
}
