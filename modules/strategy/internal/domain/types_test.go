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

func TestGroup_TableName_ShouldReturnStrategyGroupsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_groups", Group{}.TableName())
}

func TestExecutionRequest_TableName_ShouldReturnExecutionRequestsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_execution_requests", ExecutionRequest{}.TableName())
}

func TestBacktestJob_TableName_ShouldReturnBacktestJobsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_backtest_jobs", BacktestJob{}.TableName())
}
