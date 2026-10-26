package factorexpr

import (
	"github.com/mooyang-code/moox/packages/factorname"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 以下用例来自阶段 1 的 Codex 审查（2026-10-17），防止资源耗尽与语义问题回归。

// 输出数有上限，在编译任何表达式之前检查。
func TestOutputLimitCheckedBeforeCompiling(t *testing.T) {
	text := "outputs:\n"
	for i := 0; i <= MaxOutputs; i++ {
		text += "  o" + strconv.Itoa(i) + ": close\n"
	}
	start := time.Now()
	_, err := Compile(text)
	if err == nil || !strings.Contains(err.Error(), "不能超过 64 个") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("rejecting took %v", elapsed)
	}
}

// expr-lang 把 0 < e < 1 改写成两个比较并共享 e；嵌套几十层时编译必须仍是线性的。
func TestChainedComparisonDoesNotExplode(t *testing.T) {
	factor := mustCompile(t, outputSpec(`where(0 < close < 1, 1, 0)`))
	if got := factor.Outputs[0].Root.Canonical(); got != "where(and(lt(0,close),lt(close,1)),1,0)" {
		t.Fatalf("canonical = %s", got)
	}
	expression := "close"
	for i := 0; i < 30; i++ {
		expression = "where(0 < " + expression + " < 1, 1, 0)"
	}
	start := time.Now()
	_, err := Compile(outputSpec(expression))
	if err == nil || !strings.Contains(err.Error(), "超过 4096 个") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("compiling took %v", elapsed)
	}
}

// 规范文本直接写入一个缓冲区：链式 let 不再让各节点缓存的子树文本按平方增长。
func TestCanonicalTextMemoryIsLinear(t *testing.T) {
	column := strings.Repeat("x", MaxIdentifierRunes)
	var b strings.Builder
	b.WriteString("let:\n  a0: " + column + "\n")
	for i := 1; i <= 2000; i++ {
		b.WriteString("  a" + strconv.Itoa(i) + ": a" + strconv.Itoa(i-1) + " + a0\n")
	}
	b.WriteString("output: a2000\n")
	factor := mustCompile(t, b.String())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	text := factor.CanonicalText()
	runtime.ReadMemStats(&after)
	if want := 2001 * MaxIdentifierRunes; len(text) < want {
		t.Fatalf("canonical text has %d bytes, want at least %d", len(text), want)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("CanonicalText allocated %d bytes", allocated)
	}
}

func TestIdentifierLengthLimit(t *testing.T) {
	long := strings.Repeat("x", MaxIdentifierRunes+1)
	if _, err := Compile(outputSpec(long + " + 1")); err == nil || !strings.Contains(err.Error(), "最长 64 个字符") {
		t.Fatalf("long input column: err = %v", err)
	}
	longOutput := strings.Repeat("x", factorname.MaxOutputLen+1)
	if _, err := Compile("outputs:\n  " + longOutput + ": close\n  y: open\n"); err == nil || !strings.Contains(err.Error(), "超过 16 个字符") {
		t.Fatalf("long output column: err = %v", err)
	}
}

func TestSignedPowerOfZero(t *testing.T) {
	fn := Scalar("signed_power")
	if got := fn([]float64{0, -1}); !math.IsNaN(got) {
		t.Fatalf("signed_power(0, -1) = %v, want NaN", got)
	}
	if got := fn([]float64{0, 0}); got != 0 {
		t.Fatalf("signed_power(0, 0) = %v, want 0", got)
	}
	if got := fn([]float64{0, 2}); got != 0 {
		t.Fatalf("signed_power(0, 2) = %v, want 0", got)
	}
	if _, err := Compile(outputSpec(`close + signed_power(0, -1)`)); err == nil {
		t.Fatal("constant signed_power(0, -1) should be rejected as non-finite")
	}
}

// where 是条件选择（设计 4.4）：未选分支的 NaN 不影响结果；条件是常量时折叠掉的分支也就不是依赖。
func TestWhereSelectsBranch(t *testing.T) {
	fn := Scalar("where")
	if got := fn([]float64{1, 2, math.NaN()}); got != 2 {
		t.Fatalf("where(true, 2, NaN) = %v, want 2", got)
	}
	if got := fn([]float64{0, math.NaN(), 3}); got != 3 {
		t.Fatalf("where(false, NaN, 3) = %v, want 3", got)
	}
	factor := mustCompile(t, outputSpec(`where(true, close, ts_mean(open, 20))`))
	if factor.CanonicalText() != "output = close\n" || factor.Lookback != 1 || strings.Join(factor.InputColumns, ",") != "close" {
		t.Fatalf("factor = %v, canonical %q", factor, factor.CanonicalText())
	}
}
