package builder

import (
	"context"
	"sync"
)

type deriveCompletion struct {
	mu        sync.Mutex
	remaining int
	firstErr  error
	done      chan struct{}
}

func newDeriveCompletion(items int) *deriveCompletion {
	if items < 0 {
		items = 0
	}
	completion := &deriveCompletion{
		remaining: items,
		done:      make(chan struct{}),
	}
	if items == 0 {
		close(completion.done)
	}
	return completion
}

func (c *deriveCompletion) complete(err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remaining == 0 {
		return
	}
	if c.firstErr == nil && err != nil {
		c.firstErr = err
	}
	c.remaining--
	if c.remaining == 0 {
		close(c.done)
	}
}

func (c *deriveCompletion) wait(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.done:
		c.mu.Lock()
		err := c.firstErr
		c.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
