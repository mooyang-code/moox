package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestResolveUnknownAccountLockHonorsDeadline(t *testing.T) {
	svc, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	pending, err := svc.Place(context.Background(), "space-1", testSpec(time.Unix(1_700_000_000, 0)))
	require.NoError(t, err)
	_, err = svc.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	record, err := svc.Store.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	unlock := svc.Store.LockTradingAccount(record.TradingAccountID)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.ResolveUnknown(ctx, "space-1", record.OrderID); done <- err }()
	select {
	case err = <-done:
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("ResolveUnknown waited past deadline")
	}
	unlock()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var accountErr *AccountExecutionError
	require.ErrorAs(t, err, &accountErr)
	require.Equal(t, "resolve_unknown_lock", accountErr.Operation)
	current, err := svc.Store.GetOrder(context.Background(), "space-1", record.OrderID)
	require.NoError(t, err)
	require.Equal(t, record, current)
	adapter.getErr = nil
	adapter.getResult = exchange.Order{ExchangeOrderID: "recovered-order"}
	resolved, err := svc.ResolveUnknown(context.Background(), "space-1", record.OrderID)
	require.NoError(t, err)
	require.Equal(t, "OPEN", string(resolved.State))
}
