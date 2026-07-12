package instrument

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRules_ZeroValue_ShouldBeEmpty(t *testing.T) {
	var r Rules
	assert.Empty(t, r.Version)
	assert.Empty(t, r.BaseAsset)
}
