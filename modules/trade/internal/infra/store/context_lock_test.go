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
			switch kind {
			case "account":
				acquire = func(ctx context.Context) (func(), error) { return s.LockTradingAccountContext(ctx, "a") }
			case "execution":
				acquire = func(ctx context.Context) (func(), error) { return s.LockLogicalAccountExecutionContext(ctx, "s", "l") }
			case "membership":
				acquire = s.LockLogicalAccountMembershipContext
			}
			unlock, err := acquire(context.Background())
			require.NoError(t, err)
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
