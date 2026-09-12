package inputcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Generation protects one View's disposable database. Database pointers must
// not escape Use; the callback holds a lease until all its operations finish.
type Generation struct {
	mu     sync.RWMutex
	change sync.Mutex
	root   string
	dir    string
	db     *Database
	epoch  uint64
	closed bool
}

func NewGeneration(ctx context.Context, root string, columns []Column, keys []string) (*Generation, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	g := &Generation{root: root}
	if err := g.ReplaceSchema(ctx, columns, keys); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Generation) Use(fn func(*Database, uint64) error) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.db == nil {
		return fmt.Errorf("cache generation is closed")
	}
	return fn(g.db, g.epoch)
}

func (g *Generation) ReplaceSchema(ctx context.Context, columns []Column, keys []string) error {
	g.change.Lock()
	defer g.change.Unlock()
	return g.replace(ctx, func(path string) (*Database, error) {
		return CreateDatabase(ctx, path, columns, keys)
	})
}

func (g *Generation) Rebuild(ctx context.Context, keepRows int64) error {
	g.change.Lock()
	defer g.change.Unlock()
	return g.replace(ctx, func(path string) (next *Database, err error) {
		err = g.Use(func(db *Database, _ uint64) error {
			next, err = db.Rebuild(ctx, path, keepRows)
			return err
		})
		return next, err
	})
}

func (g *Generation) replace(ctx context.Context, build func(string) (*Database, error)) error {
	if g.closed {
		return fmt.Errorf("cache generation is closed")
	}
	dir, err := os.MkdirTemp(g.root, "generation-")
	if err != nil {
		return err
	}
	next, err := build(filepath.Join(dir, "view.duckdb"))
	if err != nil {
		return errors.Join(err, os.RemoveAll(dir))
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(err, next.Close(), os.RemoveAll(dir))
	}
	g.mu.Lock()
	old, oldDir := g.db, g.dir
	g.db, g.dir = next, dir
	g.epoch++
	g.mu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			return err
		}
		return os.RemoveAll(oldDir)
	}
	return nil
}

func (g *Generation) Close() error {
	g.change.Lock()
	defer g.change.Unlock()
	g.closed = true
	g.mu.Lock()
	old, dir := g.db, g.dir
	g.db = nil
	g.mu.Unlock()
	if old == nil {
		return nil
	}
	if err := old.Close(); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
