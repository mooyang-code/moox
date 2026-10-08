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

// 省略 portfolio 字段时取默认值；显式写出的 0 按原值校验：max_missing: 0 表示不容忍缺数，leverage: 0 非法。
func TestPortfolioDefaultsAndExplicitZero(t *testing.T) {
	t.Parallel()
	rules := "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	defaults, err := Parse([]byte(rules))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if defaults.Portfolio.Leverage.String() != "1" || defaults.Portfolio.MaxMissing.String() != DefaultMaxMissing {
		t.Fatalf("默认值不符：%+v", defaults.Portfolio)
	}
	strict, err := Parse([]byte(rules + "portfolio: {max_missing: 0}\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !strict.Portfolio.MaxMissing.IsZero() || strict.Portfolio.Leverage.String() != "1" {
		t.Fatalf("显式 max_missing: 0 应保留为 0：%+v", strict.Portfolio)
	}
	if _, err := Parse([]byte(rules + "portfolio: {leverage: 0}\n")); err == nil || !strings.Contains(err.Error(), "leverage") {
		t.Fatalf("leverage: 0 应拒绝：%v", err)
	}
}

// holding 规则能同时持有 top × 批次数个标的，cap 校验按这个上限计算。
func TestHoldingCapCountsAllBatches(t *testing.T) {
	t.Parallel()
	base := "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 1, cap: 0.25}"
	if _, err := Parse([]byte(base + ", holding: {bars: 4, offsets: [0, 2]}}\n")); err != nil {
		t.Fatalf("2 个批次 × 2 个标的 × 0.25 能分完预算：%v", err)
	}
	if _, err := Parse([]byte(base + "}\n")); err == nil || !strings.Contains(err.Error(), "分不完") {
		t.Fatalf("没有 holding 时 2 × 0.25 分不完预算：%v", err)
	}
}

// 输入上限：DSL 超过 64 KiB、单条表达式超过 1024 字符都拒绝；上限内的深层括号能正常解析。
func TestParseEnforcesSizeLimits(t *testing.T) {
	t.Parallel()
	rules := "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	if _, err := Parse([]byte(rules + "# " + strings.Repeat("x", 64<<10) + "\n")); err == nil || !strings.Contains(err.Error(), "KiB") {
		t.Fatalf("超大 DSL 应拒绝：%v", err)
	}
	long := strings.Repeat("close + ", 200) + "close"
	if _, err := Parse([]byte(strings.Replace(rules, "score: close", "score: \""+long+"\"", 1))); err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("超长表达式应拒绝：%v", err)
	}
	nested := strings.Repeat("(", 300) + "close" + strings.Repeat(")", 300)
	if _, err := Parse([]byte(strings.Replace(rules, "score: close", "score: \""+nested+"\"", 1))); err != nil {
		t.Fatalf("上限内的嵌套括号应能解析：%v", err)
	}
}

// 多个阶段同时有错时总是报告固定顺序上的第一个阶段。
func TestValidationReportsStagesInFixedOrder(t *testing.T) {
	t.Parallel()
	raw := []byte("name: x\nrules:\n  - {id: a, type: rank, filter: \"close >\", score: \"close *\", select: {top: 1, where: \"score <\"}, filter_after: \"close ==\", weight: 1}\n")
	for i := 0; i < 20; i++ {
		_, err := Parse(raw)
		if err == nil || !strings.Contains(err.Error(), "的 filter 表达式无效") {
			t.Fatalf("第 %d 次应报告 filter 阶段：%v", i, err)
		}
	}
	strategy, err := Parse([]byte(exampleDSL))
	if err != nil {
		t.Fatal(err)
	}
	strategy.Rules[1].Entry = "close >"
	strategy.Rules[1].Exit = "close <"
	for i := 0; i < 20; i++ {
		if _, err := ReferencedColumns(strategy); err == nil || !strings.Contains(err.Error(), "的 entry 表达式无效") {
			t.Fatalf("第 %d 次应报告 entry 阶段：%v", i, err)
		}
	}
}
