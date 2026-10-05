package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocksCoordinateAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first := NewLocks(dir)
	second := NewLocks(dir)
	unlock, err := first.LockContext(context.Background(), "fset_prices_1m")
	require.NoError(t, err)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = second.LockContext(ctx, "fset_prices_1m")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	unlock()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err = second.LockContext(ctx, "fset_prices_1m")
	require.NoError(t, err)
	unlock()
}

func TestLockContextRejectsEmptySet(t *testing.T) {
	_, err := NewLocks("").LockContext(context.Background(), " ")
	require.Error(t, err)
	require.False(t, errors.Is(err, context.Canceled))
}

func TestLocksServeWaitersInArrivalOrder(t *testing.T) {
	locks := NewLocks("")
	unlock, err := locks.LockContext(context.Background(), "fset_prices_1m")
	require.NoError(t, err)

	order := make(chan string, 2)
	acquired := make(chan struct{})
	go func() {
		release, lockErr := locks.LockContext(context.Background(), "fset_prices_1m")
		require.NoError(t, lockErr)
		order <- "live"
		close(acquired)
		release()
	}()
	time.Sleep(50 * time.Millisecond)

	// A recalc worker releasing and immediately re-acquiring must queue behind
	// the live waiter instead of winning the race.
	unlock()
	recalcRelease, err := locks.LockContext(context.Background(), "fset_prices_1m")
	require.NoError(t, err)
	order <- "recalc"
	recalcRelease()
	<-acquired
	require.Equal(t, "live", <-order)
}

func TestFactorLockKeysDoNotCollideWithSetKeys(t *testing.T) {
	locks := NewLocks(t.TempDir())
	unlockFactor, err := locks.LockFactorContext(context.Background(), "fset_prices_1m")
	require.NoError(t, err)
	defer unlockFactor()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlockSet, err := locks.LockContext(ctx, "fset_prices_1m")
	require.NoError(t, err, "a factor lock must not block the set lock with the same name")
	unlockSet()

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = NewLocks(locks.dir).LockFactorContext(ctx, "fset_prices_1m")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = locks.LockFactorContext(context.Background(), " ")
	require.Error(t, err)
}
