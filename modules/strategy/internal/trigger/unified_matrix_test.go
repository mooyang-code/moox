package trigger

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

// 本文件补齐方式 A、B 对照的组合矩阵（第二轮总审查）：选币市场不可用、新上永续与永续下架在其他模式下、持有的永续空头或多头遇到
// 永续下架、永续整体不可用而持有的空头不在永续本期的基础集合里。每个场景先断言两种方式一致，再核对结论本身。

// ucLongShortDSL 是一条多头、一条空头规则（都按 m 选 1 个）的多空策略。
func ucLongShortDSL(mode string) string {
	return "name: compare\nmarket: " + mode + "\nrules:\n  - id: l\n    type: rank\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 0.5}\n  - id: s\n    type: rank\n    side: short\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 0.5}\nportfolio: {leverage: 2, max_missing: 1}\n"
}

// spot/swap（选币市场是现货）：现货因子整体被跳过时没有候选池，整期 factor_skipped；现货因子在每个读行的币上都失败（整体失败）时整期
// market_failed。两条路径的原因不同是设计如此（前者是就绪判断的原因，后者是输入装配判出的市场不可用），两种方式各自一致。
func TestUnifiedMatrixSelectionMarketUnavailable(t *testing.T) {
	skipped := ucBase("{select: spot, prefer: swap}")
	skipped.skipped["spot"] = true
	if decision, _ := requireSame(t, skipped); decision.Status != engine.StatusSkipped || decision.SkipReason != readiness.ReasonFactorSkipped {
		t.Fatalf("现货因子被跳过应整期 factor_skipped：%s(%s)", decision.Status, decision.SkipReason)
	}
	failed := ucBase("{select: spot, prefer: swap}")
	failed.failed["spot"] = []string{"A", "B", "C", "X"}
	failed.rows["spot"] = map[string]float64{}
	if decision, _ := requireSame(t, failed); decision.Status != engine.StatusSkipped || decision.SkipReason != input.SkipMarketFailed {
		t.Fatalf("现货整体失败应整期 market_failed：%s(%s)", decision.Status, decision.SkipReason)
	}
}

// 新上永续（年龄不够）、永续下架而现货还在（无持仓）在 both/spot 与 spot/swap 下：不能借用另一个市场的历史或残留行。
func TestUnifiedMatrixNewAndDelistedSwapInOtherModes(t *testing.T) {
	newListing := map[string]map[string]string{
		"{select: both, prefer: spot}": {"D|swap": "0.5", "X|spot": "0.5"},
		"{select: spot, prefer: swap}": {"C|swap": "0.5", "X|spot": "0.5"},
	}
	for mode, want := range newListing {
		world := ucBase(mode)
		world.dsl = ucDSL(mode, true)
		world.history = map[string][]string{"spot": {"A", "B", "C", "X"}, "swap": {"B", "C", "D"}}
		if _, targets := requireSame(t, world); !reflect.DeepEqual(targets, want) {
			t.Fatalf("%s 新上永续：got %v want %v", mode, targets, want)
		}
	}
	delisted := map[string]map[string]string{
		"{select: both, prefer: spot}": {"D|swap": "0.5", "Y|spot": "0.5"},
		"{select: spot, prefer: swap}": {"X|swap": "0.5", "Y|spot": "0.5"},
	}
	for mode, want := range delisted {
		world := ucBase(mode)
		world.members = map[string][]string{"spot": {"A", "B", "C", "X", "Y"}, "swap": {"B", "C", "D", "X"}}
		world.rows["spot"]["Y"] = 9
		world.rows["swap"]["Y"] = 10
		if _, targets := requireSame(t, world); !reflect.DeepEqual(targets, want) {
			t.Fatalf("%s 永续下架：got %v want %v", mode, targets, want)
		}
	}
}

// 上期持有永续空头 Y，本期 Y 从永续下架（现货还在）、永续其余正常：空头规则按无行处理，Y|swap 平掉，不借用现货行继续做空；
// 多头规则照常可以在现货选 Y。三种含永续下单的模式一样。
func TestUnifiedMatrixDelistedSwapHeldShortIsClosed(t *testing.T) {
	cases := map[string]map[string]string{
		"{select: both, prefer: spot}": {"D|swap": "-0.5", "Y|spot": "0.5"},
		"{select: spot, prefer: swap}": {"X|swap": "-0.5", "Y|spot": "0.5"},
		"{select: both, prefer: swap}": {"X|swap": "-0.5", "Y|spot": "0.5"},
	}
	for mode, want := range cases {
		world := ucBase(mode)
		world.dsl = ucLongShortDSL(mode)
		world.members = map[string][]string{"spot": {"A", "B", "C", "X", "Y"}, "swap": {"B", "C", "D", "X"}}
		world.rows["spot"]["Y"] = 9
		world.state = engine.State{Rules: map[string]engine.RuleState{"s": {Held: []string{"Y"}}}, Targets: map[string]string{"Y|swap": "-0.5"}}
		if _, targets := requireSame(t, world); !reflect.DeepEqual(targets, want) {
			t.Fatalf("%s：got %v want %v", mode, targets, want)
		}
	}
}

// 上期持有永续多头 Y，本期 Y 从永续下架（现货还在）：粘住永续、永续没有行，按无行平掉，不退回现货的行。
func TestUnifiedMatrixDelistedSwapHeldLongIsClosed(t *testing.T) {
	cases := map[string]map[string]string{
		"{select: both, prefer: spot}": {"D|swap": "0.5", "X|spot": "0.5"},
		"{select: spot, prefer: swap}": {"C|swap": "0.5", "X|swap": "0.5"},
		"{select: both, prefer: swap}": {"D|swap": "0.5", "X|swap": "0.5"},
	}
	for mode, want := range cases {
		world := ucBase(mode)
		world.members = map[string][]string{"spot": {"A", "B", "C", "X", "Y"}, "swap": {"B", "C", "D", "X"}}
		world.rows["spot"]["Y"] = 9
		world.state = engine.State{Rules: map[string]engine.RuleState{"r": {Held: []string{"Y"}}}, Targets: map[string]string{"Y|swap": "0.5"}}
		if _, targets := requireSame(t, world); !reflect.DeepEqual(targets, want) {
			t.Fatalf("%s：got %v want %v", mode, targets, want)
		}
	}
}

// 永续整体不可用（因子被跳过、或整体失败），空头规则持有 X 与 Y，Y 已不在永续本期的基础集合里（标签刚移除）：输入层给上期在永续持仓的
// 标的也补上 market_unavailable 证据，Y 与 X 一样冻结，不借用现货行重新排名；空头规则不守门，多头照常在现货开仓。
func TestUnifiedMatrixSwapDownFreezesHeldShortOutsideSwapUniverse(t *testing.T) {
	for _, path := range []string{"skipped", "wholly_failed"} {
		t.Run(path, func(t *testing.T) {
			mode := "{select: both, prefer: spot}"
			world := ucBase(mode)
			world.dsl = ucLongShortDSL(mode)
			world.members = map[string][]string{"spot": {"A", "B", "C", "X", "Y"}, "swap": {"B", "C", "D", "X"}}
			world.rows["spot"]["Y"] = 9
			if path == "skipped" {
				world.skipped["swap"] = true
			} else {
				world.failed["swap"] = []string{"B", "C", "D", "X"}
				world.rows["swap"] = map[string]float64{}
			}
			world.state = engine.State{Rules: map[string]engine.RuleState{"s": {Held: []string{"X", "Y"}}}, Targets: map[string]string{"X|swap": "-0.25", "Y|swap": "-0.25"}}
			_, targets := requireSame(t, world)
			want := map[string]string{"X|swap": "-0.25(hold)", "Y|swap": "-0.25(hold)", "C|spot": "0.5"}
			if !reflect.DeepEqual(targets, want) {
				t.Fatalf("got %v want %v", targets, want)
			}
		})
	}
}
