package dsl

import (
	"strconv"
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
		"缺少 score":                   {rules: "  - {id: a, type: rank, select: {top: 1}, weight: 1}\n", want: "缺少 score"},
		"缺少 select":                  {rules: "  - {id: a, type: rank, score: close, weight: 1}\n", want: "缺少 select"},
		"top 与 bottom 同时":            {rules: "  - {id: a, type: rank, score: close, select: {top: 1, bottom: 1}, weight: 1}\n", want: "top 或 bottom"},
		"id 非法":                      {rules: "  - {id: A-1, type: rank, score: close, select: {top: 1}, weight: 1}\n", want: "snake_case"},
		"id 重复":                      {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.5}\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.5}\n", want: "重复"},
		"type 未知":                    {rules: "  - {id: a, type: trend, score: close, select: {top: 1}, weight: 1}\n", want: "type"},
		"signal 缺 pool":              {rules: "  - {id: a, type: signal, entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n", want: "pool"},
		"signal 带 score":             {rules: "  - {id: a, type: signal, pool: [BTC-USDT], score: close, entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n", want: "只能使用"},
		"signal 缺 exit":              {rules: "  - {id: a, type: signal, pool: [BTC-USDT], entry: \"close > 1\", weight: 1}\n", want: "entry 和 exit"},
		"rank 带 entry":               {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, entry: \"close > 1\"}\n", want: "entry"},
		"cap 分不完":                    {rules: "  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 0.8, cap: 0.3}}\n", want: "分不完"},
		"cap 超过 total":               {rules: "  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 0.5, cap: 0.6}}\n", want: "cap"},
		"预算超过杠杆":                     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.7}\n  - {id: b, type: rank, score: close, select: {top: 1}, weight: 0.7}\n", want: "leverage"},
		"holding 与 buffer":           {rules: "  - {id: a, type: rank, score: close, select: {top: 1, buffer: 1}, weight: 1, holding: {bars: 4, offsets: [0]}}\n", want: "holding"},
		"holding 偏移越界":               {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: {bars: 4, offsets: [4]}}\n", want: "offsets"},
		"side 非法":                    {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, side: flat}\n", want: "side"},
		"method 非法":                  {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: {total: 1, method: score}}\n", want: "method"},
		"rank 用在 filter":             {rules: "  - {id: a, type: rank, filter: \"rank(close) > 0.5\", score: close, select: {top: 1}, weight: 1}\n", want: "只能在 score"},
		"score 用在 filter":            {rules: "  - {id: a, type: rank, filter: \"score > 0\", score: close, select: {top: 1}, weight: 1}\n", want: "score 只能"},
		"bar 非法":                     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "bar: 7x\n", want: "可选：1m、5m、15m、30m、1h、4h、1d"},
		"max_weight 超杠杆":             {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {max_weight: 1.5}\n", want: "max_weight"},
		"max_missing 越界":             {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {max_missing: 2}\n", want: "max_missing"},
		"杠杆超过上限":                     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {leverage: 11}\n", want: "不能超过 10"},
		"top 超过上限":                   {rules: "  - {id: a, type: rank, score: close, select: {top: 10001}, weight: 1}\n", want: "不能超过 10000"},
		"holding.bars 超过上限":          {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: {bars: 10001, offsets: [0]}}\n", want: "holding.bars 不能超过"},
		"min_age_bars 超过上限":          {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "universe: {min_age_bars: 10001}\n", want: "min_age_bars 不能超过"},
		"固定 pool 小于 min_universe":    {rules: "  - {id: a, type: rank, pool: [A, B], score: close, select: {top: 1}, weight: 1}\n", portfolio: "portfolio: {min_universe: 3}\n", want: "少于 portfolio.min_universe=3"},
		"权重量级过大":                     {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1e300000}\n", want: "指数的绝对值不能超过 30"},
		"holding.offsets 过多":         {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: {bars: 100, offsets: [" + offsetsList(65) + "]}}\n", want: "holding.offsets 不能超过 64 个"},
		"只写 include 小于 min_universe": {rules: "  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", portfolio: "universe: {include: [A, B, C], exclude: [C]}\nportfolio: {min_universe: 3}\n", want: "只由 include 给出 2 个标的"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(base + tc.rules + tc.portfolio))
			if err == nil {
				t.Fatalf("Parse() 不应接受无效的 DSL")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，应包含 %q", err, tc.want)
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
		t.Fatalf("Parse() 出错：%v", err)
	}
	if strategy.Rules[0].Holding == nil || len(strategy.Rules[0].Holding.Offsets) != 4 || strategy.Rules[1].Pool.Tags[0] != "majors" {
		t.Fatalf("规则不符：%+v", strategy.Rules)
	}
}

// 省略 portfolio 字段时取默认值；显式写出的 0 按原值校验：max_missing: 0 表示不容忍缺数，leverage: 0 非法。
func TestPortfolioDefaultsAndExplicitZero(t *testing.T) {
	t.Parallel()
	rules := "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	defaults, err := Parse([]byte(rules))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if defaults.Portfolio.Leverage.String() != "1" || defaults.Portfolio.MaxMissing.String() != DefaultMaxMissing {
		t.Fatalf("默认值不符：%+v", defaults.Portfolio)
	}
	strict, err := Parse([]byte(rules + "portfolio: {max_missing: 0}\n"))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if !strict.Portfolio.MaxMissing.IsZero() || strict.Portfolio.Leverage.String() != "1" {
		t.Fatalf("显式 max_missing: 0 应保留为 0：%+v", strict.Portfolio)
	}
	if _, err := Parse([]byte(rules + "portfolio: {leverage: 0}\n")); err == nil || !strings.Contains(err.Error(), "leverage") {
		t.Fatalf("leverage: 0 应拒绝：%v", err)
	}
}

// holding 规则的每个批次各自按 cap/total 配权，批次之间互不补足，所以 cap 校验不能按批次数放宽。
func TestHoldingCapIsCheckedPerBatch(t *testing.T) {
	t.Parallel()
	base := "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 2}, weight: {total: 1, cap: 0.25}"
	if _, err := Parse([]byte(base + ", holding: {bars: 4, offsets: [0, 2]}}\n")); err == nil || !strings.Contains(err.Error(), "分不完") {
		t.Fatalf("2 个批次不能让 top 2 × 0.25 分完预算：%v", err)
	}
	if _, err := Parse([]byte(base + "}\n")); err == nil || !strings.Contains(err.Error(), "分不完") {
		t.Fatalf("没有 holding 时 2 × 0.25 分不完预算：%v", err)
	}
	ok := strings.Replace(base, "cap: 0.25", "cap: 0.5", 1)
	if _, err := Parse([]byte(ok + ", holding: {bars: 4, offsets: [0, 2]}}\n")); err != nil {
		t.Fatalf("top 2 × 0.5 能分完预算：%v", err)
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

// YAML 合法的数字写法都按定点数接受；超出定点精度或分数形式拒绝。
func TestNumericLiterals(t *testing.T) {
	t.Parallel()
	for _, literal := range []string{".5", "+0.5", "5e-1", "0.50", "\"0.5\""} {
		strategy, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: " + literal + "}\n"))
		if err != nil {
			t.Fatalf("%s 应被接受：%v", literal, err)
		}
		if got := strategy.Rules[0].Weight.Total.String(); got != "0.5" {
			t.Fatalf("%s 应解析为 0.5，实际 %s", literal, got)
		}
	}
	for _, literal := range []string{"1e-19", "\"1/2\"", "\"0x1p-1\"", "\"inf\""} {
		if _, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: " + literal + "}\n")); err == nil {
			t.Fatalf("%s 应被拒绝", literal)
		}
	}
}

// 标的与标签 ID 区分大小写：去重与匹配使用同一规则。
func TestIdentifierListsAreCaseSensitive(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: signal, pool: [BTC-USDT, btc-usdt], entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n"))
	if err != nil || len(strategy.Rules[0].Pool.Fixed) != 2 {
		t.Fatalf("大小写不同的两项都应保留：%+v err=%v", strategy.Rules, err)
	}
	if _, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: signal, pool: [BTC-USDT, BTC-USDT], entry: \"close > 1\", exit: \"close < 1\", weight: 1}\n")); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("完全相同的两项应拒绝：%v", err)
	}
}

// 表达式上限：长度按字符计、只校验用户原文；语法节点数在保存时检查；bars[...] 只接受 .列名；负数先报。
func TestExpressionLimitsAtSaveTime(t *testing.T) {
	t.Parallel()
	rules := "name: x\nrules:\n  - {id: a, type: rank, score: SCORE, select: {top: 1}, weight: 1}\n"
	parse := func(score string) error {
		_, err := Parse([]byte(strings.Replace(rules, "SCORE", "\""+score+"\"", 1)))
		return err
	}
	terms := make([]string, 70)
	for i := range terms {
		terms[i] = "aaaaaaaaaaaaa"
	}
	if err := parse("rank(" + strings.Join(terms, "+") + ")"); err != nil {
		t.Fatalf("原文不超过上限的截面函数参数不应因重新序列化变长而被拒：%v", err)
	}
	if err := parse("instrument_id == '" + strings.Repeat("比", 1000) + "'"); err != nil {
		t.Fatalf("1000 个汉字的表达式按字符计未超限：%v", err)
	}
	if err := parse(strings.Repeat("a", 1025)); err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("超过 1024 个字符应拒绝：%v", err)
	}
	if err := parse(strings.TrimSuffix(strings.Repeat("a+", 200), "+")); err == nil || !strings.Contains(err.Error(), "过于复杂") {
		t.Fatalf("超过节点上限应在保存时拒绝：%v", err)
	}
	if err := parse("bars[0][close]"); err == nil || !strings.Contains(err.Error(), ".列名") {
		t.Fatalf("bars[0][close] 应拒绝：%v", err)
	}
	if _, err := Parse([]byte("name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: -1}, weight: 1}\n")); err == nil || !strings.Contains(err.Error(), "负数") {
		t.Fatalf("负数应明确报出：%v", err)
	}
}

// offsetsList 返回 0 到 n-1 的逗号分隔列表。
func offsetsList(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, strconv.Itoa(i))
	}
	return strings.Join(parts, ", ")
}

// rankRule 返回只有一条 rank 规则 a 的 DSL；body 是规则里 id、type、score 之外的字段（flow 写法）。
func rankRule(body string) string {
	return "name: x\nrules:\n  - {id: a, type: rank, score: close, " + body + "}\n"
}

// optionCase 是第 3 期新增字段的一条保存校验用例：dsl 应被拒绝，错误包含 want。
type optionCase struct {
	dsl  string
	want string
}

func runOptionCases(t *testing.T, cases map[string]optionCase) {
	t.Helper()
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.dsl))
			if err == nil {
				t.Fatalf("Parse() 不应接受：%s", tc.dsl)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，应包含 %q", err, tc.want)
			}
		})
	}
}

// S1 选择数量：互斥与非法值。
func TestValidateSelectOptions(t *testing.T) {
	t.Parallel()
	runOptionCases(t, map[string]optionCase{
		"top 写 1 以上的小数":        {rankRule("select: {top: 2.5}, weight: 1"), "正整数或 0～1 之间的比例"},
		"top 为负":               {rankRule("select: {top: -0.1}, weight: 1"), "不能为负数"},
		"count_round 非法":       {rankRule("select: {top: 0.1, count_round: up}, weight: 1"), "floor、ceil 或 round"},
		"count_min 配整数 top":    {rankRule("select: {top: 3, count_min: 1}, weight: 1"), "写成比例时"},
		"count_min 为负":         {rankRule("select: {top: 0.1, count_min: -1}, weight: 1"), "count_min 必须在"},
		"range 与 top 同写":       {rankRule("select: {top: 2, range: [1, 3]}, weight: 1"), "range 与 top、bottom 只能写一个"},
		"range 倒序":             {rankRule("select: {range: [5, 3]}, weight: 1"), "结束名次不小于起始"},
		"range 从 0 开始":         {rankRule("select: {range: [0, 3]}, weight: 1"), "名次从 1 开始"},
		"range 不是两项":           {rankRule("select: {range: [1, 2, 3]}, weight: 1"), "两个整数"},
		"range 写小数":            {rankRule("select: {range: [1.5, 3]}, weight: 1"), "必须是整数"},
		"range 与 buffer":       {rankRule("select: {range: [1, 3], buffer: 1}, weight: 1"), "range 不能与 buffer"},
		"group max 与 quotas":   {rankRule("select: {top: 3, group: {by: g, max: 1, quotas: [1]}}, weight: 1"), "max 与 quotas 只能写一个"},
		"group 缺 by":           {rankRule("select: {top: 3, group: {max: 1}}, weight: 1"), "缺少 by"},
		"group 缺名额":            {rankRule("select: {top: 3, group: {by: g}}, weight: 1"), "max 或 quotas"},
		"group by 写表达式":        {rankRule("select: {top: 3, group: {by: \"g + 1\", max: 1}}, weight: 1"), "必须是 View 里的列名"},
		"group max 为 0":        {rankRule("select: {top: 3, group: {by: g, max: 0}}, weight: 1"), "max 必须在 1 到"},
		"group rest 配 max":     {rankRule("select: {top: 3, group: {by: g, max: 1, rest: 1}}, weight: 1"), "rest 只能与 quotas"},
		"group quotas 为负":      {rankRule("select: {top: 3, group: {by: g, quotas: [1, -1]}}, weight: 1"), "quotas[1] 必须在"},
		"group 未知字段":           {rankRule("select: {top: 3, group: {by: g, max: 1, size: 2}}, weight: 1"), "未知字段"},
		"then 与 buffer":        {rankRule("select: {top: 3, buffer: 1, then: {score: close, top: 1}}, weight: 1"), "then 不能与 buffer"},
		"then 缺 score":         {rankRule("select: {top: 3, then: {top: 1}}, weight: 1"), "缺少 score"},
		"then top 与 bottom 同写": {rankRule("select: {top: 3, then: {score: close, top: 1, bottom: 1}}, weight: 1"), "top 或 bottom 之一"},
		"then 用 score 标识符":     {rankRule("select: {top: 3, then: {score: \"score\", top: 1}}, weight: 1"), "score 只能"},
		"then 个数按 cap 检查":      {rankRule("select: {top: 3, then: {score: close, top: 2}}, weight: {total: 1, cap: 0.4}"), "分不完"},
		"then 比例大于 1":          {rankRule("select: {top: 3, then: {score: close, top: 1.5}}, weight: 1"), "正整数或 0～1"},
		"then 整数配 count_round": {rankRule("select: {top: 3, then: {score: close, top: 1, count_round: ceil}}, weight: 1"), "只能在 top/bottom 写成比例时使用"},
		"then count_round 非法":  {rankRule("select: {top: 3, then: {score: close, top: 0.5, count_round: up}}, weight: 1"), "count_round 只能是"},
		"then count_min 为负":    {rankRule("select: {top: 3, then: {score: close, top: 0.5, count_min: -1}}, weight: 1"), "count_min 必须在"},
	})
}

// S1 选择数量：正常写法保存通过，个数换算与引用列正确；写比例时跳过 cap 的保存检查。
func TestValidateSelectOptionsAccepted(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(`name: x
rules:
  - id: a
    type: rank
    score: close
    select: {top: 0.1, count_round: ceil, count_min: 2, group: {by: ind, quotas: [2, 1], rest: 1}}
    weight: {total: 0.5, cap: 0.1}
  - id: b
    type: rank
    score: close
    select: {range: [3, 6], group: {by: ind, max: 1}, then: {score: "rank(-close)", top: 3}}
    weight: 0.5
`))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	sel := strategy.Rules[0].Select
	if !sel.IsRatio() || sel.Count(25) != 3 || sel.Count(5) != 2 {
		t.Fatalf("比例换算不对：%+v", sel)
	}
	if count, fixed := strategy.Rules[1].Select.FixedCount(); !fixed || count != 4 || strategy.Rules[1].Select.Then.Count(0) != 3 {
		t.Fatalf("range 的个数应为 4：%d %v", count, fixed)
	}
	columns, err := ReferencedColumns(strategy)
	if err != nil || strings.Join(columns, ",") != "close,ind" {
		t.Fatalf("引用列应包含 group.by：%v %v", columns, err)
	}
	if _, err := Compile(strategy, []string{"close"}); err == nil || !strings.Contains(err.Error(), `"ind"`) {
		t.Fatalf("group.by 的列不存在时编译应报错：%v", err)
	}
	boolean, err := Parse([]byte(rankRule(`select: {top: 3, then: {score: "close > 1", top: 1}}, weight: 1`)))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if _, err := Compile(boolean, []string{"close"}); err == nil || !strings.Contains(err.Error(), "select.then.score") {
		t.Fatalf("then.score 返回布尔时编译应报错：%v", err)
	}
}

// then 的 top、bottom 可以写比例：个数按第一段选中个数换算，保存时不做 cap 检查（个数要到运行时才知道）。
func TestValidateSelectThenRatio(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(rankRule(`select: {top: 6, then: {score: close, top: 0.5, count_round: ceil, count_min: 2}}, weight: {total: 1, cap: 0.1}`)))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	then := strategy.Rules[0].Select.Then
	if count, fixed := then.FixedCount(); fixed || count != 0 {
		t.Fatalf("比例写法不应有固定个数：%d %v", count, fixed)
	}
	for sample, want := range map[int]int{6: 3, 5: 3, 1: 2, 0: 2} {
		if got := then.Count(sample); got != want {
			t.Fatalf("Count(%d) = %d，应为 %d", sample, got, want)
		}
	}
}

// S5 换仓相位与日历周期：互斥与非法值。
func TestValidateHoldingCalendarOptions(t *testing.T) {
	t.Parallel()
	holding := func(body string) string { return rankRule("select: {top: 1}, weight: 1, holding: " + body) }
	runOptionCases(t, map[string]optionCase{
		"calendar 与 bars":        {holding("{calendar: week, days: [1], bars: 5}"), "calendar 与 bars、offsets 只能写一种"},
		"calendar 非法":            {holding("{calendar: year, days: [1]}"), "trading_days、week 或 month"},
		"trading_days 缺 every":   {holding("{calendar: trading_days, days: [1]}"), "holding.every 必须在 1 到"},
		"every 配 week":           {holding("{calendar: week, every: 2, days: [1]}"), "every 只能与 calendar: trading_days"},
		"days 为空":                {holding("{calendar: week, days: []}"), "days 不能为空"},
		"days 超出 every":          {holding("{calendar: trading_days, every: 5, days: [6]}"), "1 到 every=5"},
		"trading_days 写倒数":       {holding("{calendar: trading_days, every: 5, days: [-1]}"), "1 到 every=5"},
		"week 的 days 越界":         {holding("{calendar: week, days: [8]}"), "1 到 7 之间，或 -1 到 -7"},
		"month 的 days 越界":        {holding("{calendar: month, days: [-32]}"), "1 到 31 之间，或 -1 到 -31"},
		"days 重复":                {holding("{calendar: month, days: [1, 1]}"), "重复值 1"},
		"at 非法":                  {holding("{calendar: week, days: [1], at: noon}"), "first 或 last"},
		"at 配 bars":              {holding("{bars: 4, offsets: [0], at: last}"), "只能与 holding.calendar 一起写"},
		"anchor 配 week":          {holding("{calendar: week, days: [1], anchor: \"2024-01-01\"}"), "anchor 不能与 calendar: week"},
		"anchor 写法非法":            {holding("{bars: 4, offsets: [0], anchor: \"yesterday\"}"), "RFC3339"},
		"calendar 与 buffer":      {rankRule("select: {top: 1, buffer: 1}, weight: 1, holding: {calendar: week, days: [1]}"), "holding 和 select.buffer"},
		"写了 market 时锚点不是 bar 起点": {"name: x\nbar: 4h\nmarket: {select: swap, prefer: swap}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: {bars: 3, offsets: [0], anchor: \"2024-01-01T01:00:00Z\"}}\n", "不是 4h 周期的 bar 起点"},
	})
}

// S5：正常写法保存通过。
func TestValidateHoldingCalendarAccepted(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"{calendar: trading_days, every: 5, days: [1, 3], anchor: \"2024-01-02\", at: first}",
		"{calendar: week, days: [1, -1]}",
		"{calendar: month, days: [-1]}",
		"{bars: 4, offsets: [0, 2], anchor: \"2024-01-01T04:00:00Z\"}",
	} {
		strategy, err := Parse([]byte("name: x\nbar: 4h\nmarket: {select: swap, prefer: swap}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, holding: " + body + "}\n"))
		if err != nil {
			t.Fatalf("Parse(%s) = %v", body, err)
		}
		if len(strategy.Rules[0].Holding.BatchKeys()) == 0 {
			t.Fatalf("批次键不能为空：%s", body)
		}
	}
}

// S2 进出场持有：互斥与非法值。
func TestValidateHoldOptions(t *testing.T) {
	t.Parallel()
	runOptionCases(t, map[string]optionCase{
		"hold 与 buffer":           {rankRule("select: {top: 2, buffer: 1}, weight: 1, hold: {exit: \"close > 1\"}"), "hold 不能与 select.buffer"},
		"hold 与 then":             {rankRule("select: {top: 2, then: {score: close, top: 1}}, weight: 1, hold: {}"), "hold 不能与 select.then"},
		"hold 与 range":            {rankRule("select: {range: [1, 2]}, weight: 1, hold: {}"), "hold 不能与 select.range"},
		"hold 与 filter_after":     {rankRule("select: {top: 2}, filter_after: \"close > 1\", weight: 1, hold: {}"), "hold 不能与 filter_after"},
		"hold 与 holding.bars":     {rankRule("select: {top: 2}, weight: 1, hold: {}, holding: {bars: 4, offsets: [0]}"), "hold 不能与 holding 的 bars/offsets"},
		"hold 与日历多个批次":            {rankRule("select: {top: 2}, weight: 1, hold: {}, holding: {calendar: week, days: [1, 3]}"), "holding.days 只能有一个值"},
		"hold.max_bars 为负":        {rankRule("select: {top: 2}, weight: 1, hold: {max_bars: -1}"), "hold.max_bars 必须在 0 到"},
		"hold.cooldown_bars 超过上限": {rankRule("select: {top: 2}, weight: 1, hold: {cooldown_bars: 10001}"), "hold.cooldown_bars 必须在 0 到"},
		"hold 未知字段":               {rankRule("select: {top: 2}, weight: 1, hold: {keep: 1}"), "未知字段"},
		"hold.entry 用截面函数":        {rankRule("select: {top: 2}, weight: 1, hold: {entry: \"rank(close) > 0.5\"}"), "只能在 score"},
		"signal 写 hold":           {"name: x\nrules:\n  - {id: a, type: signal, pool: [A], entry: \"close > 1\", exit: \"close < 1\", weight: 1, hold: {}}\n", "不能写 hold"},
	})
	strategy, err := Parse([]byte(rankRule("select: {top: 0.1, group: {by: ind, max: 2}}, weight: 1, hold: {entry: \"a > 0\", exit: \"b > 0\", max_bars: 720, cooldown_bars: 3}, holding: {calendar: week, days: [1]}")))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	columns, err := ReferencedColumns(strategy)
	if err != nil || strings.Join(columns, ",") != "a,b,close,ind" {
		t.Fatalf("hold 的 entry、exit 列应计入引用列：%v %v", columns, err)
	}
}

// S3 规则级标的池、年龄上限、跨市场年龄、允许空选择：互斥与非法值。
func TestValidateRuleUniverseOptions(t *testing.T) {
	t.Parallel()
	runOptionCases(t, map[string]optionCase{
		"max_age_bars 不写 allow_empty":     {rankRule("select: {top: 1}, weight: 1, universe: {max_age_bars: 30}"), "必须同时写 allow_empty: true"},
		"策略级 max_age_bars 不写 allow_empty": {"name: x\nuniverse: {max_age_bars: 30}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n", "必须同时写 allow_empty: true"},
		"max_age_bars 为负":                 {rankRule("select: {top: 1}, weight: 1, allow_empty: true, universe: {max_age_bars: -1}"), "max_age_bars 不能为负数"},
		"max_age_bars 超过上限":               {rankRule("select: {top: 1}, weight: 1, allow_empty: true, universe: {max_age_bars: 10001}"), "max_age_bars 不能超过"},
		"min 不小于 max":                     {rankRule("select: {top: 1}, weight: 1, allow_empty: true, universe: {min_age_bars: 30, max_age_bars: 30}"), "min_age_bars 必须小于 max_age_bars"},
		"继承的 min 不小于规则 max":               {"name: x\nuniverse: {min_age_bars: 40}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, allow_empty: true, universe: {max_age_bars: 30}}\n", "实际使用的 universe.min_age_bars 必须小于"},
		"age_markets 非法":                  {rankRule("select: {top: 1}, weight: 1, universe: {age_markets: any}"), "each 或 all"},
		"显式 pool 写标签":                     {rankRule("pool: [A, B], select: {top: 1}, weight: 1, universe: {tags: [x]}"), "规则级 universe 只能写 min_age_bars"},
		"规则 universe 未知字段":                {rankRule("select: {top: 1}, weight: 1, universe: {size: 3}"), "未知字段"},
		"allow_empty 不是布尔":                {rankRule("select: {top: 1}, weight: 1, allow_empty: 1"), "true 或 false"},
		"策略池写 max_age_bars":               {"name: p\nbar: 1h\nuniverse: {max_age_bars: 3}\nstrategy_pool:\n  members: [{id: a, strategy: s1}]\n  allocation: {method: fixed, ratios: {a: 1}}\n", "策略池不写 universe"},
	})
	// 显式 pool 的规则可以写年龄类字段；allow_empty 的规则不受 pool 小于 min_universe 的保存检查限制。
	strategy, err := Parse([]byte("name: x\nportfolio: {min_universe: 5}\nrules:\n  - {id: a, type: rank, pool: [A, B], score: close, select: {top: 1}, weight: 1, allow_empty: true, universe: {min_age_bars: 1, max_age_bars: 20, age_markets: all}}\n"))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	merged := strategy.RuleUniverse(strategy.Rules[0])
	if merged.MinAgeBars != 1 || merged.MaxAgeBars != 20 || !merged.AllMarkets() || !strategy.HasRuleAges() || strategy.LongestAgeBars() != 20 {
		t.Fatalf("规则级年龄字段不对：%+v", merged)
	}
	plain, err := Parse([]byte("name: x\nuniverse: {min_age_bars: 5}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, universe: {exclude: [B]}}\n"))
	if err != nil || plain.HasRuleAges() {
		t.Fatalf("只改标的范围不算规则级年龄：%v", err)
	}
}

// S6 仓位系数与缺数冻结：非法值与互斥。
func TestValidateScaleOptions(t *testing.T) {
	t.Parallel()
	runOptionCases(t, map[string]optionCase{
		"scale 缺 expr":         {rankRule("select: {top: 1}, weight: 1, scale: {max: 1}"), "缺少 expr"},
		"scale.max 为 0":        {rankRule("select: {top: 1}, weight: 1, scale: {expr: c, max: 0}"), "max 必须在 (0, 10]"},
		"scale.max 超过 10":      {rankRule("select: {top: 1}, weight: 1, scale: {expr: c, max: 11}"), "max 必须在 (0, 10]"},
		"scale 用截面函数":          {rankRule("select: {top: 1}, weight: 1, scale: {expr: \"rank(c)\"}"), "只能在 score"},
		"scale 未知字段":           {rankRule("select: {top: 1}, weight: 1, scale: {expr: c, min: 0}"), "未知字段"},
		"keep_bars 为 0":        {rankRule("select: {top: 1}, weight: 1, on_missing: {keep_bars: 0}"), "keep_bars 必须在 1 到 1000"},
		"keep_bars 超过上限":       {rankRule("select: {top: 1}, weight: 1, on_missing: {keep_bars: 1001}"), "keep_bars 必须在 1 到 1000"},
		"on_missing 未知字段":      {rankRule("select: {top: 1}, weight: 1, on_missing: {cash: true}"), "未知字段"},
		"tranche 写 on_missing": {"name: x\nrules:\n  - id: t\n    type: tranche\n    weight: 1\n    on_missing: {keep_bars: 3}\n    tranche: {hold_period: 1, long_factors: [{column: close}], long_coin_num: 1, long_weight: 1, stg_weight: 1}\n", "不支持 on_missing"},
	})
	strategy, err := Parse([]byte(rankRule("select: {top: 1}, weight: 1, scale: {expr: \"coef\", max: 2}, on_missing: {keep_bars: 5}")))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if strategy.Rules[0].Scale.Max != 2 || strategy.Rules[0].OnMissing.KeepBars != 5 || !strategy.HasOnMissing() {
		t.Fatalf("scale、on_missing 解析不对：%+v", strategy.Rules[0])
	}
	columns, err := ReferencedColumns(strategy)
	if err != nil || strings.Join(columns, ",") != "close,coef" {
		t.Fatalf("scale 的列应计入引用列：%v %v", columns, err)
	}
	defaulted, err := Parse([]byte(rankRule("select: {top: 1}, weight: 1, scale: {expr: coef}")))
	if err != nil || defaulted.Rules[0].Scale.Max != 1 {
		t.Fatalf("scale.max 默认 1：%v", err)
	}
}
