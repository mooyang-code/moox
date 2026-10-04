package selection

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

func TestExplicitEmptyPoolProducesNoTargets(t *testing.T) {
	for name, pool := range map[string]config.Pool{
		"fixed": {Fixed: []string{}},
		"udf":   {UDF: &config.PoolUDF{Name: "empty"}},
	} {
		t.Run(name, func(t *testing.T) {
			dsl := config.DSL{Rules: map[string]config.Rule{
				"empty": {Pool: pool, WeightEach: "0.1"},
			}}
			got, err := Evaluate(dsl, EvaluationInput{Rows: []Row{makeRow("BTC", "close", "1")}})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Targets) != 0 {
				t.Fatalf("empty pool produced targets: %#v", got.Targets)
			}
		})
	}
}

func TestExplicitEmptyPoolSkipsExpressionsWithoutRows(t *testing.T) {
	top := 1
	dsl := config.DSL{Rules: map[string]config.Rule{
		"empty": {
			Pool:         config.Pool{Fixed: []string{}},
			FilterBefore: "turnover_20 > 100",
			Score:        "return_20",
			Select:       &config.Select{Where: "score > 0", Top: &top},
			Weight:       "1",
		},
	}}
	got, err := Evaluate(dsl, EvaluationInput{Rows: []Row{makeRow("BTC", "close", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 0 {
		t.Fatalf("empty pool produced targets: %#v", got.Targets)
	}
}

func TestInstrumentIDIsAvailableToExpressions(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC", "ETH"}, PoolSet: true, FilterBefore: `instrument_id == "BTC"`, WeightEach: "0.1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{makeRow("BTC", "close", "1"), makeRow("ETH", "close", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "BTC" {
		t.Fatalf("instrument_id filter result = %#v", got.Targets)
	}
}

func TestExpressionRewritesDoNotTouchStringLiterals(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"bars[-1]"}, PoolSet: true, FilterBefore: `instrument_id == "bars[-1]"`, WeightEach: "0.1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{{InstrumentID: "bars[-1]", Values: map[string]quant.Decimal{"close": quant.Must("1")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "bars[-1]" {
		t.Fatalf("string literal was rewritten: %#v", got.Targets)
	}
}

func TestExpressionQuotePlaceholdersAreNotRecursivelyRewritten(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {
			Pool: []string{"__moox_quote_1__", "bars[-1]"}, PoolSet: true,
			FilterBefore: `instrument_id == "__moox_quote_1__" || instrument_id == "bars[-1]"`, WeightEach: "0.1",
		},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		{InstrumentID: "__moox_quote_1__", Values: map[string]quant.Decimal{"close": quant.Must("1")}},
		{InstrumentID: "bars[-1]", Values: map[string]quant.Decimal{"close": quant.Must("1")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 2 {
		t.Fatalf("placeholder literal was rewritten: %#v", got.Targets)
	}
}

func TestExpressionQuotePlaceholderCannotCollideWithFieldName(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC"}, PoolSet: true, FilterBefore: `__moox_quote_0__ > 1 && instrument_id == "BTC"`, WeightEach: "0.1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{{
		InstrumentID: "BTC",
		Values:       map[string]quant.Decimal{"__moox_quote_0__": quant.Must("2")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("field name collided with quote placeholder: %#v", got.Targets)
	}
}

func TestExpressionGuardsDoNotTreatStringLiteralsAsIdentifiers(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC"}, PoolSet: true, FilterBefore: `instrument_id == "score"`, WeightEach: "0.1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{makeRow("score", "close", "1"), makeRow("BTC", "close", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 0 {
		t.Fatalf("string literal score was treated as score identifier: %#v", got.Targets)
	}
}

func TestNormalizerDetectionDoesNotTreatStringLiteralsAsCalls(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC"}, PoolSet: true, Score: `instrument_id == "zscore(foo)" ? 1 : 0`, WeightEach: "0.1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{makeRow("BTC", "close", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].TargetWeight != "0.1" {
		t.Fatalf("score string literal was treated as normalizer call: %#v", got.Targets)
	}
}

func TestPoolMatchingIsCaseInsensitive(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"btc-usdt"}, PoolSet: true, WeightEach: "1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{makeRow("BTC-USDT", "close", "1")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "BTC-USDT" {
		t.Fatalf("case-insensitive pool result = %#v", got.Targets)
	}
}

func TestRuleSkipsRowsWithoutScopedFactorValue(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC", "ETH"}, PoolSet: true, Score: "factor_a", Select: Select{Top: 1}, Weight: "1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		{InstrumentID: "BTC", Values: map[string]quant.Decimal{"factor_a": quant.Must("2")}},
		{InstrumentID: "ETH", Values: map[string]quant.Decimal{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "BTC" {
		t.Fatalf("scoped factor candidate result = %#v", got.Targets)
	}
}

func TestNormalizeAndSelectContract(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"momentum": {Pool: []string{"A", "B", "C"}, Score: "0.6 * pct_rank(return_20) + 0.4 * pct_rank(turnover_20)", Select: Select{Top: 2}, Weight: "1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		makeRow("A", "return_20", "0.05", "turnover_20", "10000000"),
		makeRow("B", "return_20", "0.10", "turnover_20", "2000000"),
		makeRow("C", "return_20", "0.15", "turnover_20", "5000000"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 2 || got.Targets[0].InstrumentID != "A" || got.Targets[0].TargetWeight != "0.5" || got.Targets[1].InstrumentID != "C" || got.Targets[1].TargetWeight != "0.5" {
		t.Fatalf("targets = %#v", got.Targets)
	}
	if got.Debug.Scores["momentum"]["A"] != "0.4" || got.Debug.Scores["momentum"]["B"] != "0.3" || got.Debug.Scores["momentum"]["C"] != "0.8" {
		t.Fatalf("scores = %#v", got.Debug.Scores)
	}
}

func TestPreFilterDefinesNormalizationSample(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"A", "B", "C"}, FilterBefore: "turnover_20 > 2000000", Score: "pct_rank(return_20)", Select: Select{Top: 1}, Weight: "1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		makeRow("A", "return_20", "0.05", "turnover_20", "1000000"),
		makeRow("B", "return_20", "0.10", "turnover_20", "3000000"),
		makeRow("C", "return_20", "0.15", "turnover_20", "5000000"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Debug.PreCount["r"] != 2 || len(got.Targets) != 1 || got.Targets[0].InstrumentID != "C" {
		t.Fatalf("pre-filter result = %#v, debug=%#v", got.Targets, got.Debug)
	}
}

func TestPreFilterCanRemoveRowBeforeScoreCompletenessCheck(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"A", "B"}, PoolSet: true, FilterBefore: "eligible > 0", Score: "x", Select: Select{Top: 1}, Weight: "1"},
	}}
	got, err := Evaluate(definition, input.EvaluationInput{Items: []input.InstrumentInput{
		{PoolItem: input.PoolItem{InstrumentID: "A"}, ScopedFieldsReady: true, Values: map[string]quant.Decimal{"eligible": quant.Must("0")}},
		{PoolItem: input.PoolItem{InstrumentID: "B"}, ScopedFieldsReady: true, Values: map[string]quant.Decimal{"eligible": quant.Must("1"), "x": quant.Must("2")}},
	}, PeriodEnd: "2026-01-01T00:01:00Z", DataFrequency: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "B" {
		t.Fatalf("targets = %#v", got.Targets)
	}
}

func TestMissingScoreOnPassingRowIsStrictIncomplete(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"A"}, PoolSet: true, FilterBefore: "eligible > 0", Score: "x", Select: Select{Top: 1}, Weight: "1"},
	}}
	_, err := Evaluate(definition, input.EvaluationInput{Items: []input.InstrumentInput{
		{PoolItem: input.PoolItem{InstrumentID: "A"}, ScopedFieldsReady: true, Values: map[string]quant.Decimal{"eligible": quant.Must("1")}},
	}, PeriodEnd: "2026-01-01T00:01:00Z", DataFrequency: "1m"})
	if !errors.Is(err, input.ErrStrictIncomplete) {
		t.Fatalf("expected strict incomplete, got %v", err)
	}
}

func TestSelectTailAndWhere(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Score: "x", Select: Select{Where: "score >= 0.5", Tail: 1}, Weight: "1"},
	}}
	got, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		makeRow("A", "x", "0.1"), makeRow("B", "x", "0.5"), makeRow("C", "x", "0.9"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "B" {
		t.Fatalf("tail result = %#v", got.Targets)
	}
}

func TestPostFilterKeepsVacancy(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Score: "x", Select: Select{Top: 10}, Weight: "0.60", FilterAfter: "eligible > 0"},
	}}
	rows := make([]Row, 0, 10)
	for i := 0; i < 10; i++ {
		eligible := "1"
		if i >= 8 {
			eligible = "0"
		}
		rows = append(rows, makeRow("I"+string(rune('A'+i)), "x", "1", "eligible", eligible))
	}
	got, err := Evaluate(definition, EvaluationInput{Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 8 {
		t.Fatalf("post-filter target count = %d", len(got.Targets))
	}
	for _, target := range got.Targets {
		if target.TargetWeight != "0.06" {
			t.Fatalf("post-filter reweighted %s to %s", target.InstrumentID, target.TargetWeight)
		}
	}
}

func TestZeroVarianceZScore(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{"r": {Score: "zscore(x)", WeightEach: "0.1"}}}
	evaluation, err := Evaluate(definition, EvaluationInput{Rows: []Row{makeRow("A", "x", "2"), makeRow("B", "x", "2"), makeRow("C", "x", "2")}})
	if err != nil {
		t.Fatal(err)
	}
	for id, score := range evaluation.Debug.Scores["r"] {
		if score != "0" {
			t.Fatalf("zscore(%s) = %s, want 0", id, score)
		}
	}
	one := Definition{Rules: map[string]Rule{"r": {Score: "pct_rank(x)", WeightEach: "0.1"}}}
	evaluation, err = Evaluate(one, EvaluationInput{Rows: []Row{makeRow("A", "x", "2")}})
	if err != nil || evaluation.Debug.Scores["r"]["A"] != "0.5" {
		t.Fatalf("single pct_rank = %#v, err=%v", evaluation.Debug.Scores, err)
	}
}

func TestMixedNormalizersOnSameFactorUseIndependentValues(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{"r": {Score: "pct_rank(x) + zscore(x)", WeightEach: "0.1"}}}
	evaluation, err := Evaluate(definition, EvaluationInput{Rows: []Row{
		makeRow("A", "x", "1"), makeRow("B", "x", "2"), makeRow("C", "x", "3"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := evaluation.Debug.Scores["r"]["B"]; got != "0.5" {
		t.Fatalf("mixed normalizer score for B = %s, want 0.5", got)
	}
}

func TestSignalCrossingAndExitTransitions(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"ma": {Pool: []string{"BTC"}, Signals: &Signals{
			Entry: "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close",
			Exit:  "bars[-1].ma20 >= bars[-1].close && bars[0].ma20 < bars[0].close",
		}, WeightEach: "0.1"},
	}}
	first := EvaluationInput{Rows: []Row{barRow("BTC", "9", "10", "11", "10")}, PeriodEnd: time.UnixMilli(1000)}
	evaluation, err := Evaluate(definition, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Targets) != 1 || evaluation.Targets[0].TargetWeight != "0.1" {
		t.Fatalf("entry result = %#v", evaluation.Targets)
	}
	second := EvaluationInput{Rows: []Row{barRow("BTC", "11", "10", "11", "10")}, PeriodEnd: time.UnixMilli(2000)}
	evaluation, err = Evaluate(definition, second, evaluation.RuleStates)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Targets) != 1 || len(evaluation.RuleStates["ma"].Signals) != 1 {
		t.Fatalf("sustained signal result = %#v, state=%#v", evaluation.Targets, evaluation.RuleStates)
	}
	third := EvaluationInput{Rows: []Row{barRow("BTC", "11", "10", "9", "10")}, PeriodEnd: time.UnixMilli(3000)}
	evaluation, err = Evaluate(definition, third, evaluation.RuleStates)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.Targets) != 0 || len(evaluation.RuleStates) != 0 {
		t.Fatalf("exit result = %#v, state=%#v", evaluation.Targets, evaluation.RuleStates)
	}
}

func TestHoldingOffsets(t *testing.T) {
	definition := Definition{Rules: map[string]Rule{
		"r": {Pool: []string{"BTC"}, Score: "x", Select: Select{Top: 1}, Weight: "0.60", Holding: &Holding{Bars: 24, Offsets: []int{0, 12}}},
	}}
	base := EvaluationInput{Rows: []Row{makeRow("BTC", "x", "1")}, PeriodEnd: time.UnixMilli(1000), BarIndex: 0, BarDuration: time.Hour}
	first, err := Evaluate(definition, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Targets[0].TargetWeight != "0.3" || len(first.RuleStates["r"].Batches) != 1 {
		t.Fatalf("first holding = %#v, state=%#v", first.Targets, first.RuleStates)
	}
	secondInput := base
	secondInput.BarIndex = 1
	secondInput.PeriodEnd = time.UnixMilli(3600000 + 1000)
	second, err := Evaluate(definition, secondInput, first.RuleStates)
	if err != nil {
		t.Fatal(err)
	}
	if second.Targets[0].TargetWeight != "0.3" || len(second.RuleStates["r"].Batches) != 1 {
		t.Fatalf("non-hit holding = %#v, state=%#v", second.Targets, second.RuleStates)
	}
	thirdInput := base
	thirdInput.BarIndex = 12
	thirdInput.PeriodEnd = time.UnixMilli(12*3600000 + 1000)
	third, err := Evaluate(definition, thirdInput, second.RuleStates)
	if err != nil {
		t.Fatal(err)
	}
	if third.Targets[0].TargetWeight != "0.6" || len(third.RuleStates["r"].Batches) != 2 {
		t.Fatalf("second offset holding = %#v, state=%#v", third.Targets, third.RuleStates)
	}
}

func makeRow(id string, values ...string) Row {
	result := Row{InstrumentID: id, Values: map[string]quant.Decimal{}}
	for i := 0; i+1 < len(values); i += 2 {
		result.Values[values[i]] = quant.Must(values[i+1])
	}
	return result
}

func barRow(id, previousMA, previousClose, currentMA, currentClose string) Row {
	return Row{InstrumentID: id, Values: map[string]quant.Decimal{"ma20": quant.Must(currentMA), "close": quant.Must(currentClose)}, PreviousValues: map[string]quant.Decimal{"ma20": quant.Must(previousMA), "close": quant.Must(previousClose)}}
}
