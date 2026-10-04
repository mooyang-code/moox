package partitionlock

import "sync"

// Locker serializes all local operations that publish one archive partition.
// A single instance must be shared by Writer and COS Syncer.
type Locker struct {
	locks sync.Map
}

func New() *Locker { return &Locker{} }

func (l *Locker) Lock(id string) func() {
	value, _ := l.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}
