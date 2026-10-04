package order

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubmitAccountLockHonorsDeadline(t *testing.T) {
	testPendingOrderLockDeadline(t, false)
}

func TestDiscardPendingAccountLockHonorsDeadline(t *testing.T) {
	testPendingOrderLockDeadline(t, true)
}

func testPendingOrderLockDeadline(t *testing.T, discard bool) {
	t.Helper()
	svc, db, adapter := newTestService(t)
	pending, err := svc.Place(context.Background(), "space-1", testSpec(svc.now()))
	require.NoError(t, err)
	before, err := db.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	unlock := db.LockTradingAccount(before.TradingAccountID)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if discard {
			_, err := svc.DiscardPending(ctx, "space-1", before.OrderID)
			done <- err
			return
		}
		_, err := svc.Submit(ctx, "space-1", before.OrderID)
		done <- err
	}()
	select {
	case err = <-done:
		unlock()
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("pending order operation waited for the account mutex beyond its deadline")
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var accountErr *AccountExecutionError
	require.ErrorAs(t, err, &accountErr)
	operation := "submit_lock"
	if discard {
		operation = "discard_pending_lock"
	}
	require.Equal(t, operation, accountErr.Operation)
	after, err := db.GetOrder(context.Background(), "space-1", before.OrderID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Zero(t, adapter.placeCalls)
}
