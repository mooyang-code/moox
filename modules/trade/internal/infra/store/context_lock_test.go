package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContextLocksShareRegistryAndDoNotAcquireAfterCancel(t *testing.T) {
	s := &Store{}
	for _, kind := range []string{"account", "execution", "membership"} {
		t.Run(kind, func(t *testing.T) {
			var acquire func(context.Context) (func(), error)
			var unlock func()
			switch kind {
			case "account":
				unlock = s.LockTradingAccount("a")
				acquire = func(ctx context.Context) (func(), error) { return s.LockTradingAccountContext(ctx, "a") }
			case "execution":
				unlock = s.LockLogicalAccountExecution("s", "l")
				acquire = func(ctx context.Context) (func(), error) { return s.LockLogicalAccountExecutionContext(ctx, "s", "l") }
			case "membership":
				unlock = s.LockLogicalAccountMembership()
				acquire = s.LockLogicalAccountMembershipContext
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			release, err := acquire(ctx)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Nil(t, release)
			unlock()
			release, err = acquire(ctx)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Nil(t, release)
			for i := 0; i < 10; i++ {
				retry, stop := context.WithTimeout(context.Background(), time.Second)
				release, err = acquire(retry)
				stop()
				require.NoError(t, err)
				release()
			}
		})
	}
}
