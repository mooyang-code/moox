package kline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJobDefinition_ShouldExposeKlineMetadata(t *testing.T) {
	def := NewJobDefinition()
	assert.Equal(t, "kline", def.DataType)
	assert.Equal(t, "K线", def.TypeName)
	require.NotEmpty(t, def.FrequencyOptions)
	assert.Equal(t, "1m", def.FrequencyOptions[0].Value)
}
