package store

import (
	"context"
	"sync"
	"time"
)

func (s *Store) LockTradingAccountContext(ctx context.Context, accountID string) (func(), error) {
	value, _ := s.accountLocks.LoadOrStore(accountID, &sync.Mutex{})
	return lockContext(ctx, value.(*sync.Mutex))
}

func (s *Store) LockLogicalAccountExecutionContext(ctx context.Context, spaceID, logicalAccountID string) (func(), error) {
	value, _ := s.executionLocks.LoadOrStore(spaceID+"\x00"+logicalAccountID, &sync.Mutex{})
	return lockContext(ctx, value.(*sync.Mutex))
}

func (s *Store) LockLogicalAccountMembershipContext(ctx context.Context) (func(), error) {
	return lockContext(ctx, &s.logicalMembershipLock)
}

// Poll the existing mutex: a goroutine blocked in Lock could outlive cancellation
// and later acquire a lock whose caller has already returned.
func lockContext(ctx context.Context, mutex *sync.Mutex) (func(), error) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if mutex.TryLock() {
			if err := ctx.Err(); err != nil {
				mutex.Unlock()
				return nil, err
			}
			return mutex.Unlock, nil
		}
		if timer == nil {
			timer = time.NewTimer(time.Millisecond)
		} else {
			timer.Reset(time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
