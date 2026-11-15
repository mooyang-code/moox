package adminclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRetInfoSuccess(t *testing.T) {
	assert.True(t, isRetInfoSuccess(0))
	assert.False(t, isRetInfoSuccess(1))
	assert.False(t, isRetInfoSuccess(200))
}
