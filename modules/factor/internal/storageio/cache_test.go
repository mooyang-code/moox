package storageio

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowCache_GetAndPut(t *testing.T) {
	cache := NewWindowCache()
	key := WindowKey{SpaceID: "s", SourceDataset: "d", SubjectID: "BTC", Freq: "1m"}
	_, ok := cache.Get(key)
	assert.False(t, ok)

	frame := &engine.DataFrame{}
	cache.Put(key, frame)
	got, ok := cache.Get(key)
	require.True(t, ok)
	assert.Same(t, frame, got)
}
