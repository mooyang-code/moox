package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCollectionTaskNameValidationUsesTrimmedUnicodeLength(t *testing.T) {
	assert.Equal(t, "采集任务", NormalizeCollectionTaskName(" \t采集任务 \n"))

	require.NoError(t, ValidateCollectionTaskName(strings.Repeat("任", 80)))
	require.ErrorContains(t, ValidateCollectionTaskName(strings.Repeat("任", 81)), "1-80")
	require.ErrorContains(t, ValidateCollectionTaskName(" \t\n"), "required")
}
