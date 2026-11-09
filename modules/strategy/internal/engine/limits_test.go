package engine

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

const limitedStrategy = `name: limits
rules:
  - id: big
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.6}
  - id: fill
    type: rank
    score: "m"
    select: {top: 3}
    weight: {total: 0.3}
portfolio:
  leverage: 1
  limits: {long_max: %s, overflow: {to: fill}}
`

func limitedProgram(t *testing.T, longMax string) *dsl.Program {
	t.Helper()
	return compile(t, replaceFirst(limitedStrategy, "%s", longMax), "m")
}

func replaceFirst(text, old, value string) string {
	for i := 0; i+len(old) <= len(text); i++ {
		if text[i:i+len(old)] == old {
			return text[:i] + value + text[i+len(old):]
		}
	}
	return text
}

// 普通策略的单轮转移：X 合计 0.6 + 0.1 = 0.7，上限 0.3，超出 0.4 按 fill 规则的权重比例转给它持有的其他标的 Y、Z。
func TestLimitsSingleRoundOverflowToRule(t *testing.T) {
	program := limitedProgram(t, "0.3")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"X": values("m", 3), "Y": values("m", 2), "Z": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"X": "0.3", "Y": "0.3", "Z": "0.3"})
	want := []LimitOverflow{{InstrumentID: "X", Side: dsl.SideLong, Excess: "0.4", Round: 1, To: "fill"}}
	if !reflect.DeepEqual(decision.Summary.Limits, want) {
		t.Fatalf("超限明细 %+v，期望 %+v", decision.Summary.Limits, want)
	}
}

// 多轮转移：上限 0.25，第一轮 X 超出 0.45 转给 Y、Z 后它们又超限，第二轮已经没有去向，留现金。
func TestLimitsMultiRoundLeavesCash(t *testing.T) {
	program := limitedProgram(t, "0.25")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"X": values("m", 3), "Y": values("m", 2), "Z": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"X": "0.25", "Y": "0.25", "Z": "0.25"})
	want := []LimitOverflow{
		{InstrumentID: "X", Side: dsl.SideLong, Excess: "0.45", Round: 1, To: "fill"},
		{InstrumentID: "Y", Side: dsl.SideLong, Excess: "0.075", Round: 2},
		{InstrumentID: "Z", Side: dsl.SideLong, Excess: "0.075", Round: 2},
	}
	if !reflect.DeepEqual(decision.Summary.Limits, want) {
		t.Fatalf("超限明细 %+v，期望 %+v", decision.Summary.Limits, want)
	}
	assertDecimal(t, "cash", decision.Summary.Cash, "0.25")
}

func decimals(values map[string]string) map[string]quant.Decimal {
	out := make(map[string]quant.Decimal, len(values))
	for key, value := range values {
		out[key] = quant.Must(value)
	}
	return out
}

func decimalTexts(values map[string]quant.Decimal) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		if !value.IsZero() {
			out[key] = value.String()
		}
	}
	return out
}

// 三轮转移的级联：X 超出 0.6 按 A、B 的权重（0.28、0.02）分；A 又超限，第二轮全部给 B；B 再超限，第三轮没有去向留现金。
func TestLimitsCascadeUpToThreeRounds(t *testing.T) {
	contributions := decimals(map[string]string{"X|swap": "0.9", "A|swap": "0.28", "B|swap": "0.02"})
	groups := map[string]map[string]quant.Decimal{"src": decimals(map[string]string{"X|swap": "0.9"}), "t": decimals(map[string]string{"A|swap": "0.28", "B|swap": "0.02"})}
	limits := &dsl.PortfolioLimits{LongMax: quant.Must("0.3"), HasLongMax: true, OverflowTo: "t"}
	records := applyLimits(contributions, groups, limits, nil, nil)
	if got := decimalTexts(contributions); !reflect.DeepEqual(got, map[string]string{"X|swap": "0.3", "A|swap": "0.3", "B|swap": "0.3"}) {
		t.Fatalf("截断后 %v", got)
	}
	want := []LimitOverflow{
		{InstrumentID: "X", Side: dsl.SideLong, Excess: "0.6", Round: 1, To: "t"},
		{InstrumentID: "A", Side: dsl.SideLong, Excess: "0.54", Round: 2, To: "t"},
		{InstrumentID: "B", Side: dsl.SideLong, Excess: "0.3", Round: 3},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("明细 %+v，期望 %+v", records, want)
	}
}

// 去向只取来源同方向的持仓，冻结标的不参与（既不截断也不作为去向）；空头上限独立于多头，去向没有空头时留现金。
func TestLimitsSkipFrozenAndOppositeSide(t *testing.T) {
	contributions := decimals(map[string]string{"X|swap": "0.5", "Y|swap": "0.1", "Z|swap": "-0.4"})
	groups := map[string]map[string]quant.Decimal{"t": decimals(map[string]string{"Y|swap": "0.1", "Z|swap": "-0.4"})}
	limits := &dsl.PortfolioLimits{LongMax: quant.Must("0.3"), HasLongMax: true, ShortMax: quant.Must("0.2"), HasShortMax: true, OverflowTo: "t"}
	frozen := map[string]struct{}{"Y": {}}
	delete(contributions, "Y|swap") // 冻结标的的贡献在 applyPortfolio 里先删掉
	records := applyLimits(contributions, groups, limits, frozen, nil)
	if got := decimalTexts(contributions); !reflect.DeepEqual(got, map[string]string{"X|swap": "0.3", "Z|swap": "-0.2"}) {
		t.Fatalf("截断后 %v", got)
	}
	want := []LimitOverflow{
		{InstrumentID: "X", Side: dsl.SideLong, Excess: "0.2", Round: 1},
		{InstrumentID: "Z", Side: dsl.SideShort, Excess: "0.2", Round: 1},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("明细 %+v，期望 %+v", records, want)
	}
}

// 同一个币现货与永续同方向的权重合计判上限，两边按比例削减；没写 overflow 时超出部分留现金。
func TestLimitsSumMarketsOfOneInstrument(t *testing.T) {
	contributions := decimals(map[string]string{"X|spot": "0.3", "X|swap": "0.3"})
	limits := &dsl.PortfolioLimits{LongMax: quant.Must("0.4"), HasLongMax: true}
	records := applyLimits(contributions, nil, limits, nil, nil)
	if got := decimalTexts(contributions); !reflect.DeepEqual(got, map[string]string{"X|spot": "0.2", "X|swap": "0.2"}) {
		t.Fatalf("截断后 %v", got)
	}
	if len(records) != 1 || records[0].Excess != "0.2" || records[0].To != "" {
		t.Fatalf("明细 %+v", records)
	}
}
