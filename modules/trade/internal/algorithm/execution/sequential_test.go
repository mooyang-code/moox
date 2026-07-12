package executionpolicy

import (
	"context"
	"testing"

	domain "github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSequential_Descriptor_ShouldReturnSequentialV1(t *testing.T) {
	assert.Equal(t, domain.AlgorithmDescriptor{Name: "sequential", Version: "1"}, Sequential{}.Descriptor())
}

func TestSequential_Next_EmptyReady_ShouldReturnNil(t *testing.T) {
	cmds, err := Sequential{}.Next(context.Background(), domain.ExecutionState{})
	require.NoError(t, err)
	assert.Nil(t, cmds)
}

func TestSequential_Next_WithReadySlice_ShouldReturnSubmitCommand(t *testing.T) {
	cmds, err := Sequential{}.Next(context.Background(), domain.ExecutionState{
		Ready: []domain.Slice{{ID: "slice-1"}},
	})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	assert.Equal(t, shared.ExecutionSliceID("slice-1"), cmds[0].SliceID)
	assert.Equal(t, "SUBMIT", cmds[0].Type)
}
