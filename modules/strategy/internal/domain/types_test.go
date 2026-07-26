package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStrategyDefinition_TableName_ShouldReturnStrategyDefsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_defs", StrategyDefinition{}.TableName())
}

func TestBinding_TableName_ShouldReturnStrategyBindingsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_bindings", Binding{}.TableName())
}

func TestState_TableName_ShouldReturnStrategyStatesTable(t *testing.T) {
	assert.Equal(t, "t_strategy_states", State{}.TableName())
}

func TestStrategyRun_TableName_ShouldReturnStrategyRunsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_runs", StrategyRun{}.TableName())
}

func TestTargetComparison_TableName_ShouldReturnTargetComparisonsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_target_comparisons", TargetComparison{}.TableName())
}
