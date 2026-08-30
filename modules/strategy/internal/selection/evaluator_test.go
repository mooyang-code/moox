package selection

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

func TestEvaluateRanksFullPoolBeforePreFilter(t *testing.T) {
	manifest := config.Manifest{
		Long: &config.Side{
			SideWeight: "1",
			Scores:     []config.ScoreRule{{FactorID: "score", Direction: "ascending", Weight: "1"}},
			Filters:    []config.FilterRule{{Phase: "pre", FactorID: "liquidity", ValueType: "percentile", Op: "gte", Value: "0.5"}},
			Selection:  config.SelectionRule{Mode: "count", Value: 1},
		},
	}
	in := evaluationInput(
		row("A", "spot", "score", "1", "liquidity", "1"),
		row("B", "spot", "score", "2", "liquidity", "100"),
		row("C", "spot", "score", "2", "liquidity", "100"),
	)
	got, err := Evaluate(manifest, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Debug.ScoreRanks["score"][0] != 1 || got.Debug.ScoreRanks["score"][1] != 2 || got.Debug.ScoreRanks["score"][2] != 2 {
		t.Fatalf("unexpected min ranks: %#v", got.Debug.ScoreRanks["score"])
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "B" || got.Targets[0].TargetWeight != "1" {
		t.Fatalf("unexpected targets: %#v", got.Targets)
	}
}

func TestEvaluatePostFilterDoesNotRefill(t *testing.T) {
	manifest := config.Manifest{
		Long: &config.Side{
			SideWeight: "1",
			Scores:     []config.ScoreRule{{FactorID: "score", Direction: "ascending", Weight: "1"}},
			Filters:    []config.FilterRule{{Phase: "post", FactorID: "eligible", ValueType: "value", Op: "eq", Value: "1"}},
			Selection:  config.SelectionRule{Mode: "count", Value: 2},
		},
	}
	in := evaluationInput(
		row("A", "spot", "score", "1", "eligible", "0"),
		row("B", "spot", "score", "2", "eligible", "1"),
		row("C", "spot", "score", "3", "eligible", "1"),
	)
	got, err := Evaluate(manifest, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 || got.Targets[0].InstrumentID != "B" {
		t.Fatalf("post filter unexpectedly refilled: %#v", got.Targets)
	}
	if got.Debug.SelectedCount["long"] != 2 || got.Debug.PostCount["long"] != 1 {
		t.Fatalf("unexpected stage counts: %#v", got.Debug)
	}
}

func TestEvaluateLongShortNetWeightsDeterministic(t *testing.T) {
	manifest := config.Manifest{
		Long: &config.Side{
			SideWeight: "0.5",
			Scores:     []config.ScoreRule{{FactorID: "score", Direction: "ascending", Weight: "1"}},
			Selection:  config.SelectionRule{Mode: "count", Value: 2},
		},
		Short: &config.Side{
			SideWeight: "0.5",
			Scores:     []config.ScoreRule{{FactorID: "score", Direction: "descending", Weight: "1"}},
			Selection:  config.SelectionRule{Mode: "count", Value: 2},
		},
	}
	in := evaluationInput(
		row("A", "perpetual", "score", "1"),
		row("B", "perpetual", "score", "2"),
		row("C", "perpetual", "score", "3"),
	)
	got, err := Evaluate(manifest, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Debug.Gross != "1" || got.Debug.Net != "0" {
		t.Fatalf("unexpected gross/net: %s/%s", got.Debug.Gross, got.Debug.Net)
	}
	if len(got.Targets) != 2 || got.Targets[0].InstrumentID != "A" || got.Targets[0].TargetWeight != "0.25" || got.Targets[1].InstrumentID != "C" || got.Targets[1].TargetWeight != "-0.25" {
		t.Fatalf("unexpected net targets: %#v", got.Targets)
	}
}

func evaluationInput(items ...input.InstrumentInput) input.EvaluationInput {
	return input.EvaluationInput{SpaceID: "s", StrategyID: "st", PeriodEnd: "2026-01-01T00:00:00Z", Items: items}
}

func row(id, market string, values ...string) input.InstrumentInput {
	result := input.InstrumentInput{PoolItem: input.PoolItem{InstrumentID: id, Market: market}, Values: map[string]quant.Decimal{}}
	for i := 0; i+1 < len(values); i += 2 {
		result.Values[values[i]] = quant.Must(values[i+1])
	}
	return result
}
