package quant

import (
	"errors"
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
