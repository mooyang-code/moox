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
	require.Len(t, def.Fields, 1)
	assert.Equal(t, "frequency", def.Fields[0].FieldKey, "键与真实参数 frequency 一致")
}
