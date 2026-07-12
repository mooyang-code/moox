package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFactorDef_TableName_ShouldReturnFactorDefsTable(t *testing.T) {
	assert.Equal(t, "t_factor_defs", FactorDef{}.TableName())
}

func TestFactorBinding_TableName_ShouldReturnFactorBindingsTable(t *testing.T) {
	assert.Equal(t, "t_factor_bindings", FactorBinding{}.TableName())
}
