package accountsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSyncAccountLockWaitHonorsDeadline(t *testing.T) {
	for _, kind := range []string{"membership", "execution", "account"} {
		t.Run(kind, func(t *testing.T) {
			db := openSyncStore(t)
			seedSyncAccount(t, db)
			require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
				return tx.UpdateTradingAccountReadiness("space-1", "account-1", true, 1_000, "")
			}))
			svc := &Service{Store: db, Adapters: syncAdapterSource{adapter: &lockDeadlineAdapter{}}, Fills: &consumer.Reducer{Store: db}, SessionState: readySessionState(true)}
			var unlock func()
			switch kind {
			case "membership":
				unlock = db.LockLogicalAccountMembership()
			case "execution":
				unlock = db.LockLogicalAccountExecution("space-1", "logical-1")
			case "account":
				unlock = db.LockTradingAccount("account-1")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := svc.SyncAccount(ctx, "account-1"); done <- err }()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.DeadlineExceeded)
				var accountErr *orderapp.AccountExecutionError
				require.True(t, errors.As(err, &accountErr))
				require.Equal(t, "account-1", accountErr.TradingAccountID)
				account, getErr := db.GetTradingAccountByID(context.Background(), "account-1")
				require.NoError(t, getErr)
				require.False(t, account.Ready)
				require.Contains(t, account.LastError, "context deadline exceeded")
			case <-time.After(time.Second):
				unlock()
				<-done
				t.Fatal("SyncAccount waited past deadline for " + kind + " lock")
			}
			unlock()
			recovery, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			_, err := svc.SyncAccount(recovery, "account-1")
			require.NoError(t, err)
			account, err := db.GetTradingAccountByID(context.Background(), "account-1")
			require.NoError(t, err)
			require.True(t, account.Ready)
		})
	}
}

type lockDeadlineAdapter struct{ syncAdapter }

func TestSyncAccountLockFailureRetainsCleanupDatabaseError(t *testing.T) {
	for _, stage := range []string{"read", "write"} {
		t.Run(stage, func(t *testing.T) {
			db := openSyncStore(t)
			seedSyncAccount(t, db)
			dbErr := errors.New("lock cleanup database failure")
			if stage == "read" {
				require.NoError(t, db.DBForTest().Callback().Query().Before("gorm:query").Register("test:lock_cleanup", func(query *gorm.DB) { query.AddError(dbErr) }))
			} else {
				require.NoError(t, db.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:lock_cleanup", func(query *gorm.DB) { query.AddError(dbErr) }))
			}
			unlock := db.LockLogicalAccountMembership()
			defer unlock()
			svc := &Service{Store: db, Adapters: syncAdapterSource{adapter: &lockDeadlineAdapter{}}, Fills: &consumer.Reducer{Store: db}}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err := svc.SyncAccount(ctx, "account-1")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.ErrorIs(t, err, dbErr)
			joined, ok := err.(interface{ Unwrap() []error })
			require.True(t, ok)
			require.Len(t, joined.Unwrap(), 2)
			var accountErr *orderapp.AccountExecutionError
			require.ErrorAs(t, joined.Unwrap()[0], &accountErr)
			require.False(t, errors.As(joined.Unwrap()[1], &accountErr))
		})
	}
}

func TestSyncUnknownTailPropagatesLockDeadline(t *testing.T) {
	db := openSyncStore(t)
	seedSyncAccount(t, db)
	require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "unknown-order", TradingAccountID: "account-1", ClientOrderID: "unknown-client",
			ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY", PositionSide: "NET", Quantity: "1",
			ReferencePrice: "100", ReferencePriceAt: 1_000, OwnerType: "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1", State: "SUBMIT_UNKNOWN", FilledQuantity: "0",
			ReservedAsset: "USDT", ReservedQuantity: "100", RemainingReservedQuantity: "100", Version: 1, SubmittedAt: 1_000,
		})
	}))
	adapters := syncAdapterSource{adapter: &lockDeadlineAdapter{}}
	svc := &Service{Store: db, Orders: &orderapp.Service{Store: db, Adapters: adapters}, Now: func() time.Time { return time.UnixMilli(3_000) }}
	unlock := db.LockTradingAccount("account-1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.resolveUnknownOrders(ctx, "account-1", Result{}); done <- err }()
	select {
	case err := <-done:
		unlock()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var accountErr *orderapp.AccountExecutionError
		require.ErrorAs(t, err, &accountErr)
		require.Equal(t, "resolve_unknown_lock", accountErr.Operation)
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("unknown resolution tail waited past deadline")
	}
}

func (*lockDeadlineAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}
func (*lockDeadlineAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}
