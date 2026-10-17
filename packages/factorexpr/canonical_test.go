package factorexpr

import "testing"

const biasQ = `
params: {window: 20}
outputs:
  bias_q_{window}: ts_rank(close / ts_mean(close, window) - 1, window)
`

// 规范程序文本是 definition_hash 的输入（设计 9.3）：只改注释、格式、let 名或参数名不变，改变计算的一律变化。
func TestCanonicalTextIgnoresPresentation(t *testing.T) {
	base := mustCompile(t, biasQ).CanonicalText()
	if base != "bias_q_20 = ts_rank(sub(div(close,ts_mean(close,20)),1),20)\n" {
		t.Fatalf("canonical = %q", base)
	}
	same := []string{
		"# 注释\nparams:\n  window: 20   # 窗口\noutputs:\n  bias_q_{window}: |\n    ts_rank(\n      close / ts_mean(close, window) - 1,\n      window)\n",
		"params: {w: 20}\nlet:\n  mean: ts_mean(close, w)\n  ratio: close / mean\noutputs:\n  bias_q_{w}: ts_rank(ratio - 1, w)\n",
		"outputs:\n  bias_q_20: ts_rank(close / ts_mean(close, 20) - 1.0, 4 * 5)\n",
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
		"params: {window: 21}\noutputs:\n  bias_q_20: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {window: 20}\noutputs:\n  bias_rank_{window}: ts_rank(close / ts_mean(close, window) - 1, window)\n",
		"params: {window: 20}\noutputs:\n  bias_q_{window}: ts_rank(close / ts_mean(open, window) - 1, window)\n",
		"params: {window: 20}\noutputs:\n  bias_q_{window}: ts_rank(close / ts_mean(close, window), window)\n",
	}
	for _, text := range different {
		if got := mustCompile(t, text).CanonicalText(); got == base {
			t.Errorf("canonical should change for:\n%s", text)
		}
	}
	if ExprVersion != "expr/1" {
		t.Fatalf("ExprVersion = %s", ExprVersion)
	}
}

func TestCanonicalTextOrdersOutputs(t *testing.T) {
	factor := mustCompile(t, "outputs:\n  b: ts_mean(close, 5)\n  a: ts_max(high, 3)\n")
	if got := factor.CanonicalText(); got != "b = ts_mean(close,5)\na = ts_max(high,3)\n" {
		t.Fatalf("canonical = %q", got)
	}
}
