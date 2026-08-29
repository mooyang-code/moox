package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTaskRule_TableName_ShouldReturnCollectorTaskRulesTable(t *testing.T) {
	assert.Equal(t, "t_collector_task_rules", (&TaskRule{}).TableName())
}

func TestTaskRulePrepareStateValidation(t *testing.T) {
	for _, state := range []TaskRulePrepareState{PrepareStatePending, PrepareStateWaitingView, PrepareStateReady, PrepareStateError} {
		assert.True(t, state.Valid(), state)
	}
	assert.False(t, TaskRulePrepareState("unknown").Valid())
}
