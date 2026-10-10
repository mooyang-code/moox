package proto_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStrategyProtoUsesInstanceAndResultVocabulary(t *testing.T) {
	raw, err := os.ReadFile("strategy.proto")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"message Strategy ",
		"message StrategyResult ",
		"message InstrumentTarget ",
		"rpc CreateStrategy",
		"rpc GetStrategy",
		"rpc ListStrategies",
		"message StrategyInstance ",
		"rpc CreateStrategyInstance",
		"rpc SetStrategyInstanceEnabled",
		"rpc ListStrategyResults",
		"rpc GetStrategyResult",
		"rpc ListStrategyTargets",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("strategy.proto 缺少 %q", required)
		}
	}
	for _, obsolete := range []string{
		`\bBinding\b`, `\bStrategyRun\b`, `\bStrategyState\b`, `\bdata_revision\b`,
		`\bstate_json\b`, `\bPerformance\b`, `\bSetExecutionMode\b`,
		`\bTargetPosition\b`, `\btarget_quantity\b`,
	} {
		if regexp.MustCompile(obsolete).MatchString(source) {
			t.Errorf("strategy.proto 仍包含已废弃的符号 %q", obsolete)
		}
	}
}
