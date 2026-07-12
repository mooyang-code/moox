package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPathContains(t *testing.T) {
	parent := filepath.Clean("/data/archive")
	child := filepath.Clean("/data/archive/state")
	assert.True(t, pathContains(parent, child))
	assert.False(t, pathContains("/data/other", child))
}
