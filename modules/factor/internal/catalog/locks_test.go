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
