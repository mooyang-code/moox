package partitionlock

import (
	"context"
	"sync"
)

// Locker serializes all local operations that publish one archive partition.
// A single instance must be shared by Writer and COS Syncer.
type Locker struct {
	locks sync.Map
}

func New() *Locker { return &Locker{} }

// Lock 获取分区锁；等待期间 ctx 取消或到期会立即返回错误，不再无限排队。
func (l *Locker) Lock(ctx context.Context, id string) (func(), error) {
	value, _ := l.locks.LoadOrStore(id, make(chan struct{}, 1))
	slot := value.(chan struct{})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
