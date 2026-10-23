package factorexpr

import "testing"

const biasQ = `
params: {window: {type: int, min: 2, max: 50, default: 20}}
outputs:
  bias_q: ts_rank(close / ts_mean(close, window) - 1, window)
`

// 规范程序文本是 definition_hash 的输入（设计 9.3）：只改注释、格式或 let 名不变，改变计算或参数声明的一律变化。
// 参数名是快照里的键，属于计算契约，改名也会变化。
func TestCanonicalTextIgnoresPresentation(t *testing.T) {
	base := mustCompile(t, biasQ).CanonicalText()
	if base != "$window int [2, 50] = 20\nbias_q = ts_rank(sub(div(close,ts_mean(close,$window)),1),$window)\n" {
		t.Fatalf("canonical = %q", base)
	}
	same := []string{
		"# 注释\nparams:\n  window:   # 窗口\n    type: int\n    min: 2\n    max: 50\n    default: 20\noutputs:\n  bias_q: |\n    ts_rank(\n      close / ts_mean(close, window) - 1,\n      window)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\nlet:\n  mean: ts_mean(close, window)\n  ratio: close / mean\noutputs:\n  bias_q: ts_rank(ratio - 1.0, window)\n",
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
		"params: {window: {type: int, min: 2, max: 50, default: 21}}\noutputs:\n  bias_q: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {window: {type: int, min: 2, max: 60, default: 20}}\noutputs:\n  bias_q: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  bias_q: ts_rank(close / ts_mean(close, w) - 1, w)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  bias_rank: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  bias_q: ts_rank(close / ts_mean(open, window) - 1, window)\n",
		"params: {window: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  bias_q: ts_rank(close / ts_mean(close, window), window)\n",
		"outputs:\n  bias_q: ts_rank(close / ts_mean(close, 20) - 1, 20)\n",
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

func TestCanonicalTextOrdersOutputs(t *testing.T) {
	factor := mustCompile(t, "outputs:\n  b: ts_mean(close, 5)\n  a: ts_max(high, 3)\n")
	if got := factor.CanonicalText(); got != "b = ts_mean(close,5)\na = ts_max(high,3)\n" {
		t.Fatalf("canonical = %q", got)
	}
}
