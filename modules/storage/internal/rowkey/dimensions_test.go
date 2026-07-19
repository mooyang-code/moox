package rowkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDimensionsHash_Empty_ShouldReturnPlaceholder(t *testing.T) {
	assert.Equal(t, emptyDimensionsHash, DimensionsHash(nil))
	assert.Equal(t, emptyDimensionsHash, DimensionsHash(map[string]string{}))
}

func TestDimensionsHash_SameValuesDifferentOrder_ShouldBeEqual(t *testing.T) {
	a := DimensionsHash(map[string]string{"b": "2", "a": "1"})
	b := DimensionsHash(map[string]string{"a": "1", "b": "2"})
	assert.Equal(t, a, b)
	assert.Len(t, a, 64)
}

func TestDimensionsHash_DifferentValues_ShouldDiffer(t *testing.T) {
	a := DimensionsHash(map[string]string{"exchange": "binance"})
	b := DimensionsHash(map[string]string{"exchange": "okx"})
	assert.NotEqual(t, a, b)
}
