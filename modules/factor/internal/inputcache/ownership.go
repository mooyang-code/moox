package inputcache

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const cacheLockName = ".moox-input-cache.lock"
const cacheSessionPrefix = ".moox-input-cache-session-"
const cacheOwnerName = ".owner.json"

var ErrCacheLocked = errors.New("cache directory is already owned by another manager")

type cacheOwnerMarker struct {
	Owner   string `json:"owner"`
	Version int    `json:"version"`
	Session string `json:"session"`
}

type directoryOwnership struct {
	lock    *os.File
	session string
}

// The lock file is permanent: unlinking it would allow separate inode locks.
// This protocol coordinates cooperating processes, not hostile directory edits.
func ownCacheDirectory(root string) (_ *directoryOwnership, err error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("cache root must be a directory, not a symlink")
	}
	fd, err := unix.Open(filepath.Join(root, cacheLockName), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), filepath.Join(root, cacheLockName))
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = lock.Close()
		return nil, errors.New("cache lock must be a regular, unshared file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrCacheLocked
		}
		return nil, err
	}
	ownership := &directoryOwnership{lock: lock}
	defer func() {
		if err != nil {
			err = errors.Join(err, ownership.close())
		}
	}()
	// Validate before recovery so even an unrelated symlink fails closed.
	if _, err := DirectoryBytes(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validSessionName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		owned, err := validateOwnedSession(path)
		if err != nil {
			return nil, err
		}
		if owned {
			if err := os.RemoveAll(path); err != nil {
				return nil, err
			}
		}
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	name := cacheSessionPrefix + hex.EncodeToString(token[:])
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		return nil, err
	}
	marker, err := json.Marshal(cacheOwnerMarker{Owner: "moox-factor-inputcache", Version: 1, Session: name})
	if err != nil {
		return nil, err
	}
	pending := filepath.Join(path, ".owner.pending")
	file, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(marker)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return nil, err
	}
	if err := os.Rename(pending, filepath.Join(path, cacheOwnerName)); err != nil {
		return nil, err
	}
	// Only a complete marker proves ownership. Incomplete startup directories
	// are preserved on later startup rather than guessed safe to delete.
	ownership.session = path
	return ownership, nil
}

func validSessionName(name string) bool {
	if !strings.HasPrefix(name, cacheSessionPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(name, cacheSessionPrefix)
	decoded, err := hex.DecodeString(suffix)
	return err == nil && len(decoded) == 16 && suffix == strings.ToLower(suffix)
}

func validateOwnedSession(path string) (bool, error) {
	if !validSessionName(filepath.Base(path)) {
		return false, nil
	}
	if _, err := DirectoryBytes(path); err != nil {
		return false, err
	}
	file, err := os.Open(filepath.Join(path, cacheOwnerName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return false, err
	}
	if len(data) > 4096 {
		return false, errors.New("cache ownership marker is too large")
	}
	var marker cacheOwnerMarker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return false, fmt.Errorf("invalid cache ownership marker: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return false, errors.New("invalid trailing cache ownership marker data")
	}
	if marker.Owner != "moox-factor-inputcache" || marker.Version != 1 || marker.Session != filepath.Base(path) {
		return false, errors.New("unrecognized cache ownership marker")
	}
	if err := validateSessionLayout(path); err != nil {
		return false, err
	}
	return true, nil
}

func validateSessionLayout(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && (entry.Name() == cacheOwnerName || strings.HasPrefix(entry.Name(), ".cache-write-probe-")) {
			continue
		}
		digest, decodeErr := hex.DecodeString(strings.TrimPrefix(entry.Name(), "source-"))
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "source-") || decodeErr != nil || len(digest) != 32 {
			return fmt.Errorf("unrecognized owned cache entry %q", entry.Name())
		}
		generations, err := os.ReadDir(filepath.Join(path, entry.Name()))
		if err != nil {
			return err
		}
		for _, generation := range generations {
			suffix := strings.TrimPrefix(generation.Name(), "generation-")
			if !generation.IsDir() || !strings.HasPrefix(generation.Name(), "generation-") || suffix == "" || strings.Trim(suffix, "0123456789") != "" {
				return fmt.Errorf("unrecognized cache generation %q", generation.Name())
			}
			files, err := os.ReadDir(filepath.Join(path, entry.Name(), generation.Name()))
			if err != nil {
				return err
			}
			for _, file := range files {
				if !file.IsDir() && (file.Name() == "view.duckdb" || file.Name() == "view.duckdb.wal") {
					continue
				}
				if file.IsDir() && file.Name() == "view.duckdb.tmp" {
					continue
				}
				return fmt.Errorf("unrecognized cache database file %q", file.Name())
			}
		}
	}
	return nil
}

func (o *directoryOwnership) close() error {
	var err error
	if o.session != "" {
		owned, validationErr := validateOwnedSession(o.session)
		err = validationErr
		if validationErr == nil && owned {
			err = os.RemoveAll(o.session)
		}
		o.session = ""
	}
	if o.lock != nil {
		err = errors.Join(err, unix.Flock(int(o.lock.Fd()), unix.LOCK_UN), o.lock.Close())
		o.lock = nil
	}
	return err
}
