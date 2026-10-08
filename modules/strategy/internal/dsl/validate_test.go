package dsl

import (
	"strings"
	"testing"
)

func TestValidateRejectsSemanticErrors(t *testing.T) {
	t.Parallel()
	base := "name: x\nrules:\n"
	cases := map[string]struct {
		rules     string
		portfolio string
		want      string
	}{
		"缺少 score":         {rules: "  - {id: a, type: rank, select: {top: 1}, weight: 1}\n", want: "缺少 score"},
		"缺少 select":        {rules: "  - {id: a, type: rank, score: close, weight: 1}\n", want: "缺少 select"},
		"top 与 bottom 同时":  {rules: "  - {id: a, type: rank, score: close, select: {top: 1, bottom: 1}, weight: 1}\n", want: "top 或 bottom"},
		"id 非法":            {rules: "  - {id: A-1, type: rank, score: close, select: {top: 1}, weight: 1}\n", want: "snake_case"},
		"id 重复":            {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.5}\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.5}\n", want: "重复"},
		"type 未知":          {rules: "  - {id: a, type: trend, score: close, select: {top: 1}, weight: 1}\n", want: "type"},
		"signal 缺 pool":    {rules: "  - {id: a, type: signal, entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n", want: "pool"},
		"signal 带 score":   {rules: "  - {id: a, type: signal, pool: [BTC-USDT], score: close, entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n", want: "只能使用"},
		"signal 缺 exit":    {rules: "  - {id: a, type: signal, pool: [BTC-USDT], entry: \"close > 1\", weight: 1}\n", want: "entry 和 exit"},
		"rank 带 entry":     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, entry: \"close > 1\"}\n", want: "entry"},
		"cap 分不完":          {rules: "  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 0.8, cap: 0.3}}\n", want: "分不完"},
		"cap 超过 total":     {rules: "  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 0.5, cap: 0.6}}\n", want: "cap"},
		"预算超过杠杆":           {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.7}\n  - {id: b, type: rank, score: close, select: {top: 1}, weight: 0.7}\n", want: "leverage"},
		"holding 与 buffer": {rules: "  - {id: a, type: rank, score: close, select: {top: 1, buffer: 1}, weight: 1, holding: {bars: 4, offsets: [0]}}\n", want: "holding"},
		"holding 偏移越界":     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: {bars: 4, offsets: [4]}}\n", want: "offsets"},
		"side 非法":          {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, side: flat}\n", want: "side"},
		"method 非法":        {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: {total: 1, method: score}}\n", want: "method"},
		"rank 用在 filter":   {rules: "  - {id: a, type: rank, filter: \"rank(close) > 0.5\", score: close, select: {top: 1}, weight: 1}\n", want: "只能在 score"},
		"score 用在 filter":  {rules: "  - {id: a, type: rank, filter: \"score > 0\", score: close, select: {top: 1}, weight: 1}\n", want: "score 只能"},
		"bar 非法":           {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "bar: 7x\n", want: "规范频率"},
		"max_weight 超杠杆":   {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {max_weight: 1.5}\n", want: "max_weight"},
		"max_missing 越界":   {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {max_missing: 2}\n", want: "max_missing"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(base + tc.rules + tc.portfolio))
			if err == nil {
				t.Fatalf("Parse() accepted invalid DSL")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsSwapShortAndHolding(t *testing.T) {
	t.Parallel()
	raw := `name: swap
rules:
  - id: short_rev
    type: rank
    side: short
    score: "-rank(close)"
    select: {bottom: 3}
    weight: {total: 0.5, method: rank}
    holding: {bars: 24, offsets: [0, 6, 12, 18]}
  - id: long_mom
    type: rank
    pool: {tags: [majors]}
    score: "rank(zscore(close))"
    select: {top: 3}
    weight: 0.5
portfolio: {leverage: 1}
`
	strategy, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if strategy.Rules[0].Holding == nil || len(strategy.Rules[0].Holding.Offsets) != 4 || strategy.Rules[1].Pool.Tags[0] != "majors" {
		t.Fatalf("rules = %+v", strategy.Rules)
	}
}
