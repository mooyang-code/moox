package dsl

import (
	"strings"
	"testing"
)

func TestParseExample(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(exampleDSL))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if strategy.Name != "binance_spot_momentum_1h" || strategy.Bar != "1h" {
		t.Fatalf("name/bar 不符：%q/%q", strategy.Name, strategy.Bar)
	}
	if strategy.Universe.MinAgeBars != 240 || len(strategy.Universe.ExcludeTags) != 1 || len(strategy.Universe.Exclude) != 1 {
		t.Fatalf("universe 不符：%+v", strategy.Universe)
	}
	if len(strategy.Rules) != 2 {
		t.Fatalf("规则数不符：%d", len(strategy.Rules))
	}
	rank := strategy.Rules[0]
	if rank.ID != "long_momentum" || rank.Type != RuleTypeRank || rank.Name != "多头动量选币" {
		t.Fatalf("rank 规则不符：%+v", rank)
	}
	if rank.Select.Top != 5 || rank.Select.Buffer != 2 || rank.Weight.Total.String() != "0.8" || !rank.Weight.HasCap || rank.Weight.Cap.String() != "0.3" || rank.Weight.Method != WeightMethodEqual {
		t.Fatalf("rank 规则的 select/weight 不符：%+v / %+v", rank.Select, rank.Weight)
	}
	signal := strategy.Rules[1]
	if signal.Type != RuleTypeSignal || !signal.Pool.Explicit || len(signal.Pool.Fixed) != 1 || signal.Weight.Total.String() != "0.2" || signal.Side != SideLong {
		t.Fatalf("signal 规则不符：%+v", signal)
	}
	if strategy.Portfolio.Leverage.String() != "1" || !strategy.Portfolio.HasMaxWeight || strategy.Portfolio.MaxWeight.String() != "0.3" || strategy.Portfolio.MinUniverse != 20 || strategy.Portfolio.MaxMissing.String() != "0.2" {
		t.Fatalf("portfolio 不符：%+v", strategy.Portfolio)
	}
}

func TestParseDefaultsAndShorthand(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(`name: minimal
rules:
  - id: top1
    type: rank
    score: close
    select: {top: 1}
    weight: 1
`))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	if strategy.Portfolio.Leverage.String() != "1" || strategy.Portfolio.MaxMissing.String() != "0.2" || strategy.Portfolio.HasMaxWeight {
		t.Fatalf("portfolio 默认值不符：%+v", strategy.Portfolio)
	}
	rule := strategy.Rules[0]
	if rule.Weight.Total.String() != "1" || rule.Weight.Method != WeightMethodEqual || rule.Side != SideLong || rule.Pool.Explicit {
		t.Fatalf("规则默认值不符：%+v", rule)
	}
}

func TestParseRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"空文档":              "",
		"未知顶层字段":           "name: x\ntriggers: {}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n",
		"规则为 map":          "name: x\nrules:\n  a: {type: rank}\n",
		"重复字段":             "name: x\nname: y\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n",
		"规则未知字段":           "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, signals: {}}\n",
		"多文档":              "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\n---\nname: y\n",
		"pool 既非列表也非 tags": "name: x\nrules:\n  - {id: a, type: rank, pool: BTC-USDT, score: close, select: {top: 1}, weight: 1}\n",
		"weight 不是数字":      "name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: heavy}\n",
	}
	for name, raw := range cases {
		raw := raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("Parse() 不应接受格式错误的文档")
			}
		})
	}
}

func TestHashIsStable(t *testing.T) {
	t.Parallel()
	if Hash([]byte(exampleDSL)) != Hash([]byte(exampleDSL)) || !strings.HasPrefix(Hash([]byte(exampleDSL)), "sha256:") {
		t.Fatal("Hash() 不稳定")
	}
	if Hash([]byte(exampleDSL)) == Hash([]byte(exampleDSL+"\n# x")) {
		t.Fatal("Hash() 没有随内容变化")
	}
}
