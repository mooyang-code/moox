package routeprobe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FileSnapshotStore struct {
	root  string
	clock func() time.Time
}

func NewFileSnapshotStore(root string, clock func() time.Time) *FileSnapshotStore {
	if clock == nil {
		clock = time.Now
	}
	return &FileSnapshotStore{root: filepath.Clean(strings.TrimSpace(root)), clock: clock}
}

func (store *FileSnapshotStore) Put(snapshot Snapshot) error {
	if store == nil || store.root == "" || store.root == "." {
		return errors.New("routeprobe: snapshot root is required")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	data, err := MarshalSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return fmt.Errorf("create route snapshot root: %w", err)
	}
	path := store.path(snapshot.Key)
	tmp, err := os.CreateTemp(store.root, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create route snapshot temporary file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write route snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace route snapshot: %w", err)
	}
	return nil
}

func (store *FileSnapshotStore) Get(key RouteKey) (Snapshot, bool, error) {
	if store == nil || store.root == "" || store.root == "." {
		return Snapshot{}, false, errors.New("routeprobe: snapshot root is required")
	}
	if err := key.Validate(); err != nil {
		return Snapshot{}, false, err
	}
	data, err := os.ReadFile(store.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("read route snapshot: %w", err)
	}
	snapshot, err := UnmarshalSnapshot(data)
	if err != nil {
		return Snapshot{}, false, err
	}
	if snapshot.Key != key {
		return Snapshot{}, false, errors.New("route snapshot key does not match requested key")
	}
	if !snapshot.FreshAt(store.clock()) {
		return Snapshot{}, false, nil
	}
	return snapshot, true, nil
}

func (store *FileSnapshotStore) path(key RouteKey) string {
	hash := sha256.Sum256([]byte(key.String()))
	return filepath.Join(store.root, hex.EncodeToString(hash[:])+".json")
}
