package proto_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStrategyProtoUsesRunnerAndResultVocabulary(t *testing.T) {
	raw, err := os.ReadFile("strategy.proto")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"message Strategy ",
		"message StrategyRunner ",
		"message StrategyResult ",
		"message InstrumentTarget ",
		"optional int64 command_sequence",
		"rpc CreateStrategy",
		"rpc GetStrategy",
		"rpc ListStrategies",
		"rpc CreateRunner",
		"rpc GetRunner",
		"rpc ListRunners",
		"rpc UpdateRunner",
		"rpc SetRunnerStatus",
		"rpc RunOnce",
		"rpc ListStrategyResults",
		"rpc GetStrategyResult",
		"rpc ListStrategyTargets",
		"rpc GetEngineStatus",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("strategy.proto does not contain %q", required)
		}
	}
	for _, obsolete := range []string{
		`\bBinding\b`, `\bStrategyRun\b`, `\bStrategyState\b`, `\bdata_revision\b`,
		`\bstate_json\b`, `\bPerformance\b`, `\bSetExecutionMode\b`,
		`\bTargetPosition\b`, `\btarget_quantity\b`,
	} {
		if regexp.MustCompile(obsolete).MatchString(source) {
			t.Errorf("strategy.proto still contains obsolete symbol %q", obsolete)
		}
	}
}
