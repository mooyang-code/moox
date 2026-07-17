package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/mooyang-code/moox/packages/pyruntime/transport"
)

type Key struct{ Namespace, DataRevision, SchemaHash, InputHash string }
type Handle struct {
	ID, Hash, SchemaHash, Path string
	Rows, Bytes                int64
	Encoding                   transport.Encoding
	release                    func() error
	releaseOnce                sync.Once
	releaseErr                 error
}

func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	h.releaseOnce.Do(func() {
		if h.release != nil {
			h.releaseErr = h.release()
			h.release = nil
		}
	})
	return h.releaseErr
}

// Mapped is a read-only Arrow IPC file view. Close it after all records
// returned by Reader have been released.
type Mapped struct {
	reader  *ipc.FileReader
	data    []byte
	file    *os.File
	once    sync.Once
	err     error
	release func() error
}

func (m *Mapped) Reader() *ipc.FileReader {
	if m == nil {
		return nil
	}
	return m.reader
}

func (m *Mapped) Schema() *arrow.Schema {
	if m == nil || m.reader == nil {
		return nil
	}
	return m.reader.Schema()
}

func (m *Mapped) Close() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() {
		if m.reader != nil {
			m.err = m.reader.Close()
		}
		if err := closeMappedFile(m.data, m.file); m.err == nil {
			m.err = err
		}
		m.data = nil
		m.file = nil
		if m.release != nil {
			if err := m.release(); m.err == nil {
				m.err = err
			}
			m.release = nil
		}
	})
	return m.err
}

type Store struct {
	root    string
	mu      sync.Mutex
	refs    map[string]int
	created map[string]time.Time
	ttl     time.Duration
}

func NewStore(root string) *Store {
	_ = os.MkdirAll(root, 0755)
	return &Store{root: root, refs: map[string]int{}, created: map[string]time.Time{}, ttl: 5 * time.Minute}
}
func (s *Store) Acquire(ctx context.Context, key Key, content []byte, rows int64) (*Handle, error) {
	return s.acquire(ctx, key, content, rows, "")
}

// AcquireArrow writes a typed Arrow IPC file and returns a content-addressed
// handle. Repeated acquisitions share one physical snapshot file.
func (s *Store) AcquireArrow(ctx context.Context, key Key, table transport.Table) (*Handle, error) {
	content, err := transport.EncodeArrowFile(table)
	if err != nil {
		return nil, err
	}
	return s.acquire(ctx, key, content, int64(len(table.Rows)), transport.ArrowMMap)
}

func (s *Store) acquire(ctx context.Context, key Key, content []byte, rows int64, encoding transport.Encoding) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(key.Namespace+"\x00"+key.DataRevision+"\x00"+key.SchemaHash+"\x00"+key.InputHash), content...))
	hash := hex.EncodeToString(sum[:])
	path := filepath.Join(s.root, hash+".snapshot")
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		tmp, err := os.CreateTemp(s.root, ".snapshot-")
		if err != nil {
			return nil, err
		}
		tmpPath := tmp.Name()
		if _, err = tmp.Write(content); err == nil {
			err = tmp.Sync()
		}
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
		if err = os.Rename(tmpPath, path); err != nil {
			_ = os.Remove(tmpPath)
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	s.refs[hash]++
	if _, ok := s.created[hash]; !ok {
		s.created[hash] = time.Now()
	}
	h := &Handle{ID: hash, Hash: hash, SchemaHash: key.SchemaHash, Path: path, Rows: rows, Bytes: int64(len(content)), Encoding: encoding}
	h.release = func() error { return s.release(hash, path) }
	return h, nil
}

func (s *Store) release(hash, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs[hash]--
	if s.refs[hash] <= 0 {
		delete(s.refs, hash)
		delete(s.created, hash)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Open maps an Arrow IPC file read-only. The mapping remains valid until the
// returned Mapped is closed.
func (s *Store) Open(ctx context.Context, h *Handle) (*Mapped, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h == nil || h.Path == "" {
		return nil, errors.New("pyruntime: snapshot handle is required")
	}
	s.mu.Lock()
	if s.refs[h.Hash] <= 0 {
		s.mu.Unlock()
		return nil, errors.New("pyruntime: snapshot handle is not active")
	}
	s.refs[h.Hash]++
	s.mu.Unlock()
	retained := true
	defer func() {
		if retained {
			_ = s.release(h.Hash, h.Path)
		}
	}()
	st, err := os.Stat(h.Path)
	if err != nil {
		return nil, err
	}
	if st.Size() == 0 {
		return nil, errors.New("pyruntime: empty snapshot")
	}
	data, file, err := mapReadOnlyFile(h.Path, st.Size())
	if err != nil {
		return nil, err
	}
	reader, err := ipc.NewMappedFileReader(data)
	if err != nil {
		_ = closeMappedFile(data, file)
		return nil, err
	}
	retained = false
	return &Mapped{reader: reader, data: data, file: file, release: func() error { return s.release(h.Hash, h.Path) }}, nil
}
func (s *Store) Reap() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	for hash, created := range s.created {
		if s.refs[hash] == 0 && created.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.root, hash+".snapshot"))
			delete(s.created, hash)
		}
	}
	// Also inspect files left by a previous process. The in-memory index cannot
	// know their references after a restart, so only files older than the TTL
	// are eligible and active handles in this process always win via refs.
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".snapshot" {
			continue
		}
		hash := name[:len(name)-len(filepath.Ext(name))]
		if s.refs[hash] > 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(s.created, hash)
	}
	return nil
}
