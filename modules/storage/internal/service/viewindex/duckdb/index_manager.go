//go:build cgo

package duckdb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	deviceinfra "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpc "trpc.group/trpc-go/trpc-go"
)

const indexTableName = "view_rows"

var ErrIndexClosing = errors.New("view index is closing")

type IndexManagerOptions struct {
	Root string
}

type IndexManager struct {
	root    string
	mu      sync.Mutex
	handles map[string]*managedDuckDBIndex
	closed  bool
}

type managedDuckDBIndex struct {
	store      *ViewStore
	refs       int
	closing    bool
	drained    chan struct{}
	removed    chan struct{}
	removeErr  error
	closeOnce  sync.Once
	closeError error
}

var _ viewindex.ViewIndexEngine = (*IndexManager)(nil)

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("view index root is required")
	}
	if err := os.MkdirAll(filepath.Join(root, "duckdb"), 0o755); err != nil {
		return nil, err
	}
	return &IndexManager{root: root, handles: make(map[string]*managedDuckDBIndex)}, nil
}

func (m *IndexManager) Engine() string {
	return "duckdb"
}

func (m *IndexManager) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	ref, err := validateDuckDBIndex(indexID, schema)
	if err != nil {
		return err
	}
	if err := m.Remove(ctx, indexID); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(viewindex.DuckDBPath(m.root, ref)), 0o755); err != nil {
		return err
	}
	store, release, err := m.acquire(indexID, true)
	if err != nil {
		return err
	}
	defer release()
	return store.Prepare(ctx, indexTableName, schema)
}

func (m *IndexManager) Write(ctx context.Context, indexID string, batch viewindex.BatchWrite) error {
	if len(batch.RecordRows) > 0 {
		return errors.New("duckdb view index rejects record rows")
	}
	store, release, err := m.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return store.Write(ctx, indexTableName, batch)
}

func (m *IndexManager) Apply(ctx context.Context, indexID string, batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	store, release, err := m.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return store.Apply(ctx, indexTableName, batch)
}

func (m *IndexManager) DeleteTimeSeriesRows(ctx context.Context, indexID string, rows []*pb.TimeSeriesRow) error {
	store, release, err := m.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return store.DeleteRows(ctx, indexTableName, rows)
}

func (m *IndexManager) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	path := viewindex.DuckDBPath(m.root, ref)
	freeBytes, err := deviceinfra.FreeDiskBytes(m.root)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{FreeDiskBytes: freeBytes}, nil
	} else if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	store, release, err := m.acquire(indexID, false)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats, err := store.Stat(ctx, indexTableName)
	release()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats.PhysicalBytes, err = duckDBPhysicalBytes(path)
	stats.FreeDiskBytes = freeBytes
	return stats, err
}

func (m *IndexManager) QueryTimeSeriesRows(ctx context.Context, indexID string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	store, release, err := m.acquire(indexID, false)
	if err != nil {
		return nil, nil, nil, err
	}
	defer release()
	return store.QueryTimeSeriesRows(ctx, indexTableName, req)
}

func (m *IndexManager) Remove(ctx context.Context, indexID string) error {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	path := viewindex.DuckDBPath(m.root, ref)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("duckdb index manager is closed")
	}
	handle := m.handles[indexID]
	if handle == nil {
		handle = &managedDuckDBIndex{
			closing: true,
			drained: make(chan struct{}),
			removed: make(chan struct{}),
		}
		close(handle.drained)
		m.handles[indexID] = handle
	} else if handle.closing {
		removed := handle.removed
		m.mu.Unlock()
		return m.waitForRemoval(ctx, handle, removed)
	} else {
		handle.closing = true
		handle.drained = make(chan struct{})
		handle.removed = make(chan struct{})
		handle.removeErr = nil
		if handle.refs == 0 {
			close(handle.drained)
		}
	}
	drained := handle.drained
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return m.abortRemoval(indexID, handle, ctx.Err())
	case <-drained:
	}

	removeErr := errors.Join(handle.close(), removeDuckDBFiles(path))
	m.mu.Lock()
	if m.handles[indexID] == handle {
		delete(m.handles, indexID)
	}
	handle.removeErr = removeErr
	close(handle.removed)
	m.mu.Unlock()
	return removeErr
}

func (m *IndexManager) waitForRemoval(ctx context.Context, handle *managedDuckDBIndex, removed <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-removed:
		m.mu.Lock()
		defer m.mu.Unlock()
		return handle.removeErr
	}
}

func (m *IndexManager) abortRemoval(indexID string, handle *managedDuckDBIndex, removeErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.handles[indexID]; current == handle && current.closing {
		if current.store == nil {
			delete(m.handles, indexID)
		} else {
			current.closing = false
			current.drained = nil
		}
		current.removeErr = removeErr
		close(current.removed)
	}
	return removeErr
}

func (h *managedDuckDBIndex) close() error {
	if h == nil || h.store == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.closeError = h.store.Close()
	})
	return h.closeError
}

func (m *IndexManager) List(ctx context.Context) ([]string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	spaces, err := os.ReadDir(filepath.Join(m.root, "duckdb"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spaceDir := range spaces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !spaceDir.IsDir() {
			continue
		}
		spaceID, err := decodePathID(spaceDir.Name())
		if err != nil {
			continue
		}
		views, err := os.ReadDir(filepath.Join(m.root, "duckdb", spaceDir.Name()))
		if err != nil {
			return nil, err
		}
		for _, viewDir := range views {
			if !viewDir.IsDir() {
				continue
			}
			viewID, err := decodePathID(viewDir.Name())
			if err != nil {
				continue
			}
			for _, slot := range []viewindex.Slot{viewindex.SlotA, viewindex.SlotB} {
				path := filepath.Join(m.root, "duckdb", spaceDir.Name(), viewDir.Name(), string(slot)+".duckdb")
				if _, err := os.Stat(path); err == nil {
					out = append(out, viewindex.ViewIndexID(spaceID, viewID, slot))
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *IndexManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	handles := make([]*managedDuckDBIndex, 0, len(m.handles))
	for _, handle := range m.handles {
		if !handle.closing {
			handle.closing = true
			handle.drained = make(chan struct{})
			if handle.refs == 0 {
				close(handle.drained)
			}
		}
		handles = append(handles, handle)
	}
	m.mu.Unlock()
	for _, handle := range handles {
		<-handle.drained
	}
	m.mu.Lock()
	m.handles = make(map[string]*managedDuckDBIndex)
	m.mu.Unlock()
	var err error
	for _, handle := range handles {
		err = errors.Join(err, handle.close())
	}
	return err
}

func (m *IndexManager) acquire(indexID string, create bool) (*ViewStore, func(), error) {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return nil, nil, err
	}
	path := viewindex.DuckDBPath(m.root, ref)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, errors.New("duckdb index manager is closed")
	}
	if handle := m.handles[indexID]; handle != nil {
		if handle.closing {
			return nil, nil, ErrIndexClosing
		}
		handle.refs++
		return handle.store, m.releaseFunc(indexID, handle), nil
	}
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	store, err := Open(Options{Path: path})
	if err != nil {
		return nil, nil, err
	}
	handle := &managedDuckDBIndex{store: store, refs: 1}
	m.handles[indexID] = handle
	return store, m.releaseFunc(indexID, handle), nil
}

func (m *IndexManager) releaseFunc(indexID string, handle *managedDuckDBIndex) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if handle.refs == 0 {
				return
			}
			handle.refs--
			if handle.refs == 0 && handle.closing && handle.drained != nil {
				close(handle.drained)
			}
		})
	}
}

func validateDuckDBIndex(indexID string, schema viewindex.ViewIndexSchema) (viewindex.Ref, error) {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return viewindex.Ref{}, err
	}
	if schema.Engine != "" && !strings.EqualFold(schema.Engine, "duckdb") {
		return viewindex.Ref{}, fmt.Errorf("duckdb manager rejects engine %q", schema.Engine)
	}
	if schema.SpaceID != "" && schema.SpaceID != ref.SpaceID {
		return viewindex.Ref{}, errors.New("schema space_id does not match index_id")
	}
	if schema.ViewID != "" && schema.ViewID != ref.ViewID {
		return viewindex.Ref{}, errors.New("schema view_id does not match index_id")
	}
	return ref, nil
}

func removeDuckDBFiles(path string) error {
	var err error
	for _, candidate := range []string{path, path + ".wal", path + ".tmp"} {
		if removeErr := os.Remove(candidate); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

func duckDBPhysicalBytes(path string) (uint64, error) {
	var total uint64
	for _, candidate := range []string{path, path + ".wal"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if info.Size() > 0 {
			total += uint64(info.Size())
		}
	}
	return total, nil
}

func decodePathID(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) == 0 {
		return "", errors.New("invalid encoded view path")
	}
	return string(raw), nil
}
