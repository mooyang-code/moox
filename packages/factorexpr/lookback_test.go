package factorexpr

import "testing"

func TestRecursiveSpan(t *testing.T) {
	cases := []struct {
		alpha float64
		want  int
	}{
		{EMAAlpha(20), 208},
		{SMAAlpha(20, 1), 405},
		{EMAAlpha(5), 52},
		{EMAAlpha(1), 1},
		{1, 1},
	}
	for _, tc := range cases {
		if got := RecursiveSpan(tc.alpha); got != tc.want {
			t.Errorf("RecursiveSpan(%v) = %d, want %d", tc.alpha, got, tc.want)
		}
	}
}

func TestLookbackRules(t *testing.T) {
	cases := []struct {
		expression string
		want       int
	}{
		{`close`, 1},
		{`close * 2`, 1},
		{`delay(close, 3)`, 4},
		{`delta(delay(close, 2), 3)`, 6},
		{`pct_change(close)`, 2},
		{`pct_change(close, 5)`, 6},
		{`ts_mean(close, 20)`, 20},
		{`ts_mean(close, 20) - ts_mean(close, 5)`, 20},
		{`ts_corr(close, ts_mean(volume, 10), 5)`, 14},
		{`ts_quantile(close, 30, 0.5)`, 30},
		{`ema(close, 20)`, 208},
		{`ema(close, 50000, 50000)`, 50000},
		{`ema(close, 20, 0)`, 208},
		{`ema(close, 20, 50000)`, 50000},
		{`ema(ts_mean(close, 20), 1000, 300)`, 319},
		{`sma(close, 20, 1)`, 405},
		{`ema(ts_mean(close, 20), 20)`, 227},
		{`cs_rank(ts_mean(close, 20))`, 20},
		{`ts_mean(cs_rank(pct_change(close)), 10)`, 11},
		{`where(close > delay(close, 1), ts_max(high, 5), 0)`, 5},
		{`ts_count(close > open, 10)`, 10},
	}
	for _, tc := range cases {
		factor, err := Compile(outputSpec(tc.expression))
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		if factor.Lookback != tc.want {
			t.Errorf("%s: lookback = %d, want %d", tc.expression, factor.Lookback, tc.want)
		}
	}
}

func TestFactorLookbackIsMaxOverOutputs(t *testing.T) {
	factor := mustCompile(t, "outputs:\n  short: ts_mean(close, 5)\n  long: ts_rank(ts_mean(close, 10), 10)\n")
	if factor.Lookback != 19 {
		t.Fatalf("lookback = %d, want 19", factor.Lookback)
	}
	if factor.Outputs[0].Root.Lookback != 5 || factor.Outputs[1].Root.Lookback != 19 {
		t.Fatalf("per-output lookback = %d, %d", factor.Outputs[0].Root.Lookback, factor.Outputs[1].Root.Lookback)
	}
}
