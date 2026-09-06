package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogicalAccountContextLockSharesRegistry(t *testing.T) {
	s := &Store{}
	unlock := s.LockLogicalAccount("space", "logical")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := s.LockLogicalAccountContext(ctx, "space", "logical")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, got)
	other, err := s.LockLogicalAccountContext(context.Background(), "other-space", "logical")
	require.NoError(t, err)
	other()
	unlock()
	got, err = s.LockLogicalAccountContext(context.Background(), "space", "logical")
	require.NoError(t, err)
	got()
	// A canceled waiter must never acquire the shared mutex later.
	unlocked := s.LockLogicalAccount("space", "logical")
	unlocked()
}
