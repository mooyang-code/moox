package factorexpr

import "testing"

const biasQ = `
params: {window: {type: int, min: 2, max: 50, default: 20}}
output: ts_rank(close / ts_mean(close, window) - 1, window)
`

// 规范程序文本是 definition_hash 的输入（设计 9.3）：只改注释、格式或 let 名不变，改变计算或参数声明的一律变化。
// 参数名是快照里的键，属于计算契约，改名也会变化。
func TestCanonicalTextIgnoresPresentation(t *testing.T) {
	base := mustCompile(t, biasQ).CanonicalText()
	if base != "$window int [2, 50] = 20\noutput = ts_rank(sub(div(close,ts_mean(close,$window)),1),$window)\n" {
		t.Fatalf("canonical = %q", base)
	}
	same := []string{
		"# 注释\nparams:\n  window:   # 窗口\n    type: int\n    min: 2\n    max: 50\n    default: 20\noutput: |\n    ts_rank(\n      close / ts_mean(close, window) - 1,\n      window)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\nlet:\n  mean: ts_mean(close, window)\n  ratio: close / mean\noutput: ts_rank(ratio - 1.0, window)\n",
	}
	for _, text := range same {
		if got := mustCompile(t, text).CanonicalText(); got != base {
			t.Errorf("canonical changed:\n%s\ngot  %q\nwant %q", text, got, base)
		}
	}
}

func TestCanonicalTextReflectsComputation(t *testing.T) {
	base := mustCompile(t, biasQ).CanonicalText()
	different := []string{
		"params: {window: {type: int, min: 2, max: 50, default: 21}}\noutput: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {window: {type: int, min: 2, max: 60, default: 20}}\noutput: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {w: {type: int, min: 2, max: 50, default: 20}}\noutput: ts_rank(close / ts_mean(close, w) - 1, w)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\noutput: ts_rank(close / ts_mean(open, window) - 1, window)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\noutput: ts_rank(close / ts_mean(close, window), window)\n",
		"output: ts_rank(close / ts_mean(close, 20) - 1, 20)\n",
	}
	for _, text := range different {
		if got := mustCompile(t, text).CanonicalText(); got == base {
			t.Errorf("canonical should change for:\n%s", text)
		}
	}
	if ExprVersion != "expr/2" {
		t.Fatalf("ExprVersion = %s", ExprVersion)
	}
}

// 多输出因子的输出名也是计算契约的一部分：改名会改变规范文本；单输出因子没有输出名，规范文本里固定写作 output。
func TestCanonicalTextIncludesOutputNames(t *testing.T) {
	a := mustCompile(t, "outputs:\n  mid: ts_mean(close, 5)\n  up: ts_max(high, 3)\n").CanonicalText()
	b := mustCompile(t, "outputs:\n  mid: ts_mean(close, 5)\n  top: ts_max(high, 3)\n").CanonicalText()
	if a == b {
		t.Fatal("renaming a multi-output factor's output should change the canonical text")
	}
}

func TestCanonicalTextOrdersOutputs(t *testing.T) {
	factor := mustCompile(t, "outputs:\n  b: ts_mean(close, 5)\n  a: ts_max(high, 3)\n")
	if got := factor.CanonicalText(); got != "b = ts_mean(close,5)\na = ts_max(high,3)\n" {
		t.Fatalf("canonical = %q", got)
	}
}
