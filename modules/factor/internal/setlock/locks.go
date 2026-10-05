package setlock

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

// Lock order: a definition lock (LockFactorContext) is always taken before any
// set lock, and several set locks are taken in ascending set_id order.

// Locks serializes catalog changes with live and recalculation work for a set.
// The in-process lock is a one-slot channel: blocked acquirers are served in
// arrival order, so a recalc job that releases and immediately re-acquires
// between chunks cannot starve a live period that is already waiting.
type Locks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
	dir   string
}

// New returns set locks; a non-empty lockDir adds filesystem locks shared with
// other processes (moox-factor-mgr and its CLI) that open the same database.
func New(lockDir string) *Locks { return &Locks{dir: lockDir} }

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
		l.locks = make(map[string]chan struct{})
	}
	slot := l.locks[setID]
	if slot == nil {
		slot = make(chan struct{}, 1)
		l.locks[setID] = slot
	}
	l.mu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-slot }
	if l.dir == "" {
		var once sync.Once
		return func() { once.Do(release) }, nil
	}
	fd, err := l.openFileLock(setID)
	if err != nil {
		release()
		return nil, err
	}
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if err != unix.EWOULDBLOCK && err != unix.EAGAIN && err != unix.EINTR {
			_ = unix.Close(fd)
			release()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = unix.Close(fd)
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = unix.Close(fd)
			release()
		})
	}, nil
}

// LockFactorContext acquires the lock for one definition. Its key carries a
// "factor:" prefix so it never collides with a set lock.
func (l *Locks) LockFactorContext(ctx context.Context, factorID string) (func(), error) {
	factorID = strings.TrimSpace(factorID)
	if factorID == "" {
		return nil, errors.New("factor id is required for locking")
	}
	return l.LockContext(ctx, factorLockPrefix+factorID)
}

const factorLockPrefix = "factor:"

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
