package catalog

import "sync"

// Locks serializes catalog changes with live and recalculation work for a set.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Lock acquires the stable mutex for setID and returns its unlock function.
func (l *Locks) Lock(setID string) func() {
	if l == nil {
		return func() {}
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
	lock.Lock()
	return lock.Unlock
}
