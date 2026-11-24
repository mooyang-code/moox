package jobdef

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExecutionModeValidation(t *testing.T) {
	assert.True(t, ExecutionModeCloudInvoke.Valid())
	assert.True(t, ExecutionModeCollectorLocal.Valid())
	assert.False(t, ExecutionMode("unknown").Valid())
}
