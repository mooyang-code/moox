package storageio

import (
	"sync"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
)

// WindowCache is an in-process per-symbol frame cache. M2 extends this with
// incremental tail refill and late-event invalidation.
type WindowCache struct {
	mu     sync.Mutex
	frames map[WindowKey]*engine.DataFrame
}

// NewWindowCache creates an empty window cache.
func NewWindowCache() *WindowCache {
	return &WindowCache{frames: map[WindowKey]*engine.DataFrame{}}
}

// Get returns a cached frame for a key.
func (c *WindowCache) Get(key WindowKey) (*engine.DataFrame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	frame, ok := c.frames[key]
	return frame, ok
}

// Put stores a frame for a key.
func (c *WindowCache) Put(key WindowKey, frame *engine.DataFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames[key] = frame
}
