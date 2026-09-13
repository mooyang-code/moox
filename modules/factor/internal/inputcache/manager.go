package inputcache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	ErrCacheClosed   = errors.New("cache manager is closed")
	ErrCacheDisabled = errors.New("cache is disabled")
	ErrStaleHandle   = errors.New("cache handle is stale")
	ErrWritesPaused  = errors.New("cache writes are paused")
)

type SourceKey struct{ SpaceID, ViewID string }

type managedSource struct {
	contract   string
	columns    []Column
	keys       []string
	generation *Generation
}

// Manager exclusively locks Dir and recovers positively marked orphan sessions.
// Unmarked files remain counted against capacity and are never adopted/deleted.
type Manager struct {
	mu        sync.RWMutex
	cfg       Config
	sources   map[SourceKey]*managedSource
	paused    atomic.Bool
	closed    bool
	closeErr  error
	ownership *directoryOwnership
}

// Handle is bound to one immutable source contract and physical generation.
// Rebuilds invalidate it too; callers must reacquire and re-establish coverage.
type Handle struct {
	manager *Manager
	key     SourceKey
	source  *managedSource
	epoch   uint64
}

func NewManager(cfg Config) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, sources: make(map[SourceKey]*managedSource)}
	m.paused.Store(!cfg.Enabled)
	if !cfg.Enabled {
		return m, nil
	}
	ownership, err := ownCacheDirectory(cfg.Dir)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("cache directory is not writable: %w", err)
		}
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = ownership.close()
		}
	}()
	m.ownership = ownership
	bytes, err := DirectoryBytes(cfg.Dir)
	if err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(ownership.session, ".cache-write-probe-")
	if err != nil {
		return nil, fmt.Errorf("cache directory is not writable: %w", err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return nil, fmt.Errorf("clean cache directory write probe: %w", err)
	}
	m.paused.Store(bytes > cfg.MaxBytes)
	keep = true
	return m, nil
}

// Get activates an empty generation when the contract changes. Full columns
// and primary keys must describe the same schema for the same contract. No
// rows are copied and no coverage is inferred. Busy callers use their source.
// Callers must supply the authoritative current source contract, not an event's
// version: opaque contracts cannot establish ordering or prevent rollback.
func (m *Manager) Get(ctx context.Context, key SourceKey, contract string, columns []Column, keys []string) (*Handle, error) {
	if ctx == nil {
		return nil, errors.New("cache context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key.SpaceID) == "" || strings.TrimSpace(key.ViewID) == "" || strings.TrimSpace(contract) == "" {
		return nil, errors.New("cache source and contract are required")
	}
	if !m.mu.TryLock() {
		return nil, ErrCacheBusy
	}
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrCacheClosed
	}
	if !m.cfg.Enabled {
		return nil, ErrCacheDisabled
	}
	source := m.sources[key]
	if source != nil && source.contract == contract {
		if !reflect.DeepEqual(source.columns, columns) || !reflect.DeepEqual(source.keys, keys) {
			return nil, errors.New("cache schema differs within an immutable contract")
		}
	} else {
		// Invalidate before any fallible replacement work. The authoritative
		// contract has changed even if its new schema cannot be cached.
		if source != nil {
			delete(m.sources, key)
			if err := source.generation.Close(); err != nil {
				return nil, err
			}
		}
		if m.paused.Load() {
			return nil, ErrWritesPaused
		}
		// Digest only the source identity, never interpolate caller-provided paths.
		encoded, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		root := filepath.Join(m.ownership.session, fmt.Sprintf("source-%x", sha256.Sum256(encoded)))
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(root); err != nil || !info.IsDir() {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("cache source directory is not a directory")
		}
		generation, err := NewGeneration(ctx, root, columns, keys)
		if err != nil {
			return nil, err
		}
		source = &managedSource{contract: contract, columns: append([]Column(nil), columns...), keys: append([]string(nil), keys...), generation: generation}
		m.sources[key] = source
	}
	handle := &Handle{manager: m, key: key, source: source}
	if err := source.generation.Use(func(_ *Database, epoch uint64) error { handle.epoch = epoch; return nil }); err != nil {
		return nil, err
	}
	return handle, nil
}

// Use runs a read callback. It must not mutate or retain Database; writes must
// go through Fill so capacity backpressure cannot be bypassed accidentally.
// Callbacks must not call Close or reenter the manager.
func (h *Handle) Use(fn func(*Database, uint64) error) error  { return h.use(false, fn) }
func (h *Handle) Fill(fn func(*Database, uint64) error) error { return h.use(true, fn) }

func (h *Handle) use(write bool, fn func(*Database, uint64) error) error {
	if h == nil || h.manager == nil || fn == nil {
		return errors.New("cache handle and callback are required")
	}
	m := h.manager
	if !m.mu.TryRLock() {
		return ErrCacheBusy
	}
	defer m.mu.RUnlock()
	if m.closed {
		return ErrCacheClosed
	}
	if m.sources[h.key] != h.source {
		return ErrStaleHandle
	}
	return h.source.generation.Use(func(db *Database, epoch uint64) error {
		if epoch != h.epoch {
			return ErrStaleHandle
		}
		if write && m.paused.Load() {
			return ErrWritesPaused
		}
		return fn(db, epoch)
	})
}

// Maintain accounts for the entire configured directory, including orphaned
// generations and WAL files. Errors pause writes until a successful later pass.
func (m *Manager) Maintain(ctx context.Context) (CapacityResult, error) {
	if ctx == nil {
		return CapacityResult{PauseWrites: true}, errors.New("cache context is required")
	}
	if !m.mu.TryLock() {
		m.paused.Store(true)
		return CapacityResult{PauseWrites: true}, ErrCacheBusy
	}
	defer m.mu.Unlock()
	if m.closed {
		return CapacityResult{PauseWrites: true}, ErrCacheClosed
	}
	if err := ctx.Err(); err != nil {
		m.paused.Store(true)
		return CapacityResult{PauseWrites: true}, err
	}
	generations := make([]*Generation, 0, len(m.sources))
	for _, source := range m.sources {
		generations = append(generations, source.generation)
	}
	result, err := MaintainCapacity(ctx, m.cfg, generations)
	m.paused.Store(result.PauseWrites || err != nil)
	return result, err
}

// Close drains callbacks, closes generations, removes the owned session, then
// releases the lifetime directory lock. Unmarked files are preserved.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	for _, source := range m.sources {
		m.closeErr = errors.Join(m.closeErr, source.generation.Close())
	}
	m.sources = nil
	if m.ownership != nil {
		m.closeErr = errors.Join(m.closeErr, m.ownership.close())
	}
	return m.closeErr
}
