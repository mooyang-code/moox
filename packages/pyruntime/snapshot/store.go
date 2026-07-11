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
)

type Key struct{ Namespace, DataRevision, SchemaHash, InputHash string }
type Handle struct {
	ID, Hash, SchemaHash, Path string
	Rows, Bytes                int64
	release                    func() error
}

func (h *Handle) Release() error {
	if h == nil || h.release == nil {
		return nil
	}
	f := h.release
	h.release = nil
	return f()
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
	h := &Handle{ID: hash, Hash: hash, SchemaHash: key.SchemaHash, Path: path, Rows: rows, Bytes: int64(len(content))}
	h.release = func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refs[hash]--
		if s.refs[hash] <= 0 {
			delete(s.refs, hash)
			delete(s.created, hash)
			return os.Remove(path)
		}
		return nil
	}
	return h, nil
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
	return nil
}
