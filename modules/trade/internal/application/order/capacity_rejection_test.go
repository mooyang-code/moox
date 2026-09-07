package order

import (
	"context"
	"errors"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

type capacityRejectionSyncer struct {
	calls int
	check func(context.Context, string) error
}

func (s *capacityRejectionSyncer) SyncAccount(ctx context.Context, id string) error {
	s.calls++
	return s.check(ctx, id)
}

func TestSubmitRefreshesOnlyDefinitiveBalanceRejection(t *testing.T) {
	for _, kind := range []exchange.ErrorKind{exchange.ErrorInsufficientBalance, exchange.ErrorRejected, exchange.ErrorTransportUnknown, exchange.ErrorRateLimited} {
		t.Run(string(kind), func(t *testing.T) {
			s, db, adapter := newTestService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pending, err := s.Place(ctx, "space-1", testSpec(s.now()))
			require.NoError(t, err)
			syncErr := errors.New("injected shared database error")
			syncer := &capacityRejectionSyncer{check: func(refreshCtx context.Context, id string) error {
				require.NoError(t, refreshCtx.Err(), "completed rejection must survive caller cancellation")
				_, bounded := refreshCtx.Deadline()
				require.True(t, bounded)
				unlock, ok := db.TryLockTradingAccount(id)
				require.True(t, ok, "refresh must run after releasing the order account lock")
				if ok {
					unlock()
				}
				return syncErr
			}}
			s.Syncer = syncer
			adapter.placeErr = &exchange.Error{Kind: kind}
			adapter.placeHook = cancel
			result, err := s.Submit(ctx, "space-1", string(pending.ID))
			require.True(t, exchange.IsKind(err, kind), err)
			stored, getErr := db.GetOrder(context.Background(), "space-1", string(pending.ID))
			require.NoError(t, getErr)
			if kind == exchange.ErrorInsufficientBalance {
				require.Equal(t, 1, syncer.calls)
				require.ErrorIs(t, err, syncErr, "refresh failure must not be hidden behind original rejection")
				require.Equal(t, orderdomain.Rejected, result.State)
				require.Equal(t, "0", stored.RemainingReservedQuantity)
			} else {
				require.Zero(t, syncer.calls)
				if kind != exchange.ErrorRejected {
					require.Equal(t, orderdomain.SubmitUnknown, result.State)
					require.NotEqual(t, "0", stored.RemainingReservedQuantity)
				}
			}
		})
	}
}
