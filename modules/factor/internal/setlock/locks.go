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
)

// Lock order: a definition lock (LockFactorContext) is always taken before any
// set lock, and several set locks are taken in ascending task_id order.

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

// Lock acquires the stable mutex for taskID and returns its unlock function.
func (l *Locks) Lock(taskID string) func() {
	unlock, err := l.LockContext(context.Background(), taskID)
	if err != nil {
		panic(err)
	}
	return unlock
}

// LockContext acquires the per-set in-process lock and, when configured, a
// filesystem lock shared with other moox-factor processes using the same DB.
func (l *Locks) LockContext(ctx context.Context, taskID string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("factor task id is required for locking")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]chan struct{})
	}
	slot := l.locks[taskID]
	if slot == nil {
		slot = make(chan struct{}, 1)
		l.locks[taskID] = slot
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
	file, err := l.openFileLock(taskID)
	if err != nil {
		release()
		return nil, err
	}
	// 非阻塞加锁，被占用时每 10 毫秒重试，直到拿到或 ctx 结束。
	for {
		locked, err := file.tryLock()
		if err != nil {
			_ = file.close()
			release()
			return nil, err
		}
		if locked {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.close()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			file.unlock()
			_ = file.close()
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

func (l *Locks) openFileLock(taskID string) (*fileLock, error) {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(taskID))
	return openLockFile(filepath.Join(l.dir, hex.EncodeToString(digest[:])+".lock"))
}
