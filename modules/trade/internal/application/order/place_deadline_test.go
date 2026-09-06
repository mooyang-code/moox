package order

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPlaceAccountLockHonorsDeadline(t *testing.T) {
	s, db, _ := newTestService(t)
	spec := testSpec(s.now())
	unlock := db.LockTradingAccount(spec.TradingAccountID)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.Place(ctx, "space-1", spec)
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var accountErr *AccountExecutionError
		require.ErrorAs(t, err, &accountErr)
		require.Equal(t, spec.TradingAccountID, accountErr.TradingAccountID)
		require.Equal(t, "place_lock", accountErr.Operation)
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("order admission waited for account lock beyond its deadline")
	}
	_, err := db.GetOrderByClientID(context.Background(), "space-1", spec.TradingAccountID, spec.ClientOrderID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	// Canceled admission must neither leave a background waiter nor consume the
	// client ID. A subsequent caller can still admit exactly that request.
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	_, err = s.Place(retryCtx, "space-1", spec)
	require.NoError(t, err)
}
