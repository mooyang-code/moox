package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Locks serializes catalog changes with live and recalculation work for a set.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	dir   string
}

func NewLocks(lockDir string) *Locks { return &Locks{dir: lockDir} }

// Lock acquires the stable mutex for setID and returns its unlock function.
func (l *Locks) Lock(setID string) func() {
	unlock, err := l.LockContext(context.Background(), setID)
	if err != nil {
		panic(err)
	}
	return unlock
}

// LockContext acquires the per-set in-process lock and, when configured, a
// filesystem lock shared with other moox-factor processes using the same DB.
func (l *Locks) LockContext(ctx context.Context, setID string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	setID = strings.TrimSpace(setID)
	if setID == "" {
		return nil, errors.New("factor set id is required for locking")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	lock := l.locks[setID]
	if lock == nil {
		lock = &sync.Mutex{}
		l.locks[setID] = lock
	}
	l.mu.Unlock()
	for !lock.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if l.dir == "" {
		return lock.Unlock, nil
	}
	fd, err := l.openFileLock(setID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if err != unix.EWOULDBLOCK && err != unix.EAGAIN && err != unix.EINTR {
			_ = unix.Close(fd)
			lock.Unlock()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = unix.Close(fd)
			lock.Unlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			lock.Unlock()
		})
	}, nil
}

func (l *Locks) openFileLock(setID string) (int, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return -1, err
	}
	digest := sha256.Sum256([]byte(setID))
	path := filepath.Join(l.dir, hex.EncodeToString(digest[:])+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return -1, err
	}
	return fd, nil
}
