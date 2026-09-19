package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollectionTask_TableName_ShouldReturnCollectorTasksTable(t *testing.T) {
	assert.Equal(t, "t_collector_tasks", (&CollectionTask{}).TableName())
}

func TestCollectionTaskPrepareStateValidation(t *testing.T) {
	for _, state := range []CollectionTaskPrepareState{PrepareStatePending, PrepareStateWaitingView, PrepareStateReady, PrepareStateError} {
		assert.True(t, state.Valid(), state)
	}
	assert.False(t, CollectionTaskPrepareState("unknown").Valid())
}
