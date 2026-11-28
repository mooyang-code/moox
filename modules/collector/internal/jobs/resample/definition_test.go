package resample

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewJobDefinitionExposesResampleMetadata(t *testing.T) {
	definition := NewJobDefinition()
	assert.Equal(t, "kline_resample", definition.DataType)
	assert.Equal(t, "K线重采样", definition.TypeName)
	assert.Empty(t, definition.FrequencyOptions)
}
