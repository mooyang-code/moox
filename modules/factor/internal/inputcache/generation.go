package inputcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrCacheBusy = errors.New("cache generation is busy")

// Generation serializes database access with maintenance. Cache users fail fast
// while maintenance runs so the read-through layer can use the source instead.
type Generation struct {
	gate        chan struct{}
	root        string
	dir         string
	db          *Database
	epoch       uint64
	closed      bool
	compacted   bool
	compactedAt uint64
}

func NewGeneration(ctx context.Context, root string, columns []Column, keys []string) (*Generation, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	g := &Generation{root: root, gate: make(chan struct{}, 1)}
	if err := g.ReplaceSchema(ctx, columns, keys); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Generation) Use(fn func(*Database, uint64) error) error {
	select {
	case g.gate <- struct{}{}:
		defer func() { <-g.gate }()
	default:
		return ErrCacheBusy
	}
	if g.db == nil {
		return fmt.Errorf("cache generation is closed")
	}
	return fn(g.db, g.epoch)
}

func (g *Generation) acquire(ctx context.Context) error {
	select {
	case g.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-g.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Generation) ReplaceSchema(ctx context.Context, columns []Column, keys []string) error {
	if err := g.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-g.gate }()
	return g.replace(ctx, func(path string) (*Database, error) { return CreateDatabase(ctx, path, columns, keys) })
}

func (g *Generation) Rebuild(ctx context.Context, keepRows int64) error {
	if err := g.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-g.gate }()
	if g.db == nil {
		return fmt.Errorf("cache generation is closed")
	}
	if err := g.replace(ctx, func(path string) (*Database, error) { return g.db.Rebuild(ctx, path, keepRows) }); err != nil {
		return err
	}
	g.compacted, g.compactedAt = true, g.db.mutations.Load()
	return nil
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
		closeErr := next.Close()
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return errors.Join(err, os.RemoveAll(dir))
	}
	old, oldDir := g.db, g.dir
	g.db, g.dir = next, dir
	g.epoch++
	g.compacted = false
	if old != nil {
		if err := old.Close(); err != nil {
			return err
		}
		return os.RemoveAll(oldDir)
	}
	return nil
}

func (g *Generation) Close() error {
	if err := g.acquire(context.Background()); err != nil {
		return err
	}
	defer func() { <-g.gate }()
	g.closed = true
	old, dir := g.db, g.dir
	g.db = nil
	if old == nil {
		return nil
	}
	if err := old.Close(); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
