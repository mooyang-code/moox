package writer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTempFileHelpers(t *testing.T) {
	assert.True(t, isTempFile(".part.tmp-123.parquet"))
	assert.False(t, isTempFile("202601.parquet"))
	assert.True(t, containsTempMarker("dir/.tmp-123"))
	assert.False(t, containsTempMarker("clean.parquet"))
}
