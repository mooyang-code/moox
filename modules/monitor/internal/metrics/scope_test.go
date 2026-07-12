package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateMetricSpace(t *testing.T) {
	require.NoError(t, ValidateMetricSpace(InternalMetricSpaceID))
	err := ValidateMetricSpace("default")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMetricSpaceScope)
}
