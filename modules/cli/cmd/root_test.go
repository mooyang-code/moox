package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVersionInfoDefaultsToDev(t *testing.T) {
	old := Version
	Version = ""
	t.Cleanup(func() { Version = old })
	assert.Equal(t, "moox CLI dev", GetVersionInfo())
	assert.Equal(t, "moox CLI dev", GetFullVersionInfo())
}

func TestGetFullVersionInfoDefaultsToDev(t *testing.T) {
	old := Version
	Version = ""
	t.Cleanup(func() { Version = old })
	assert.Equal(t, "moox CLI dev", GetFullVersionInfo())
}

func TestPadLineAndDisplayWidth(t *testing.T) {
	assert.Equal(t, 5, calculateDisplayWidth("hello"))
	padded := padLine("x", 5)
	assert.Equal(t, 5, calculateDisplayWidth(padded))
}

func TestCalculateDisplayWidthCountsWideChars(t *testing.T) {
	assert.Equal(t, 4, calculateDisplayWidth("测试"))
}
