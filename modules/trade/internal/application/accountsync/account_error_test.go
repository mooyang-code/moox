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
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type boundarySyncAdapter struct {
	*syncAdapter
	stage string
	cause error
}

type failedSyncAdapterSource struct{ cause error }

func (s failedSyncAdapterSource) Adapter(string) (execution.ExecutionAdapter, error) {
	return nil, s.cause
}

func TestSyncAccountMissingAdapterClearsReadiness(t *testing.T) {
	db := openSyncStore(t)
	seedSyncAccount(t, db)
	require.NoError(t, db.DBForTest().Exec("UPDATE t_trading_accounts SET c_ready=1").Error)
	cause := errors.New("session adapter missing")
	s := &Service{Store: db, Fills: &consumer.Reducer{Store: db}, Adapters: failedSyncAdapterSource{cause: cause}}
	_, err := s.SyncAccount(context.Background(), "account-1")
	require.ErrorIs(t, err, cause)
	account, err := db.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, cause.Error(), account.LastError)
}

func (a boundarySyncAdapter) failure(stage string) error {
	if a.stage == stage {
		return a.cause
	}
	return nil
}
func (a boundarySyncAdapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	if a.stage == "deadline" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, a.failure("open_orders")
}

func TestSyncAccountDeadlinePersistsAccountFailure(t *testing.T) {
	db := openSyncStore(t)
	seedSyncAccount(t, db)
	s := &Service{Store: db, Fills: &consumer.Reducer{Store: db}, Adapters: syncAdapterSource{adapter: boundarySyncAdapter{syncAdapter: &syncAdapter{}, stage: "deadline"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := s.SyncAccount(ctx, "account-1")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, accountScoped := err.(*orderapp.AccountExecutionError)
	require.True(t, accountScoped, "expired caller context must not invent a persistence failure: %v", err)
	account, err := db.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Contains(t, account.LastError, context.DeadlineExceeded.Error())
}
func (a boundarySyncAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, a.failure("positions")
}
func (a boundarySyncAdapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	snapshot, _ := a.syncAdapter.GetAccountSnapshot(ctx)
	return snapshot, a.failure("snapshot")
}
func (a boundarySyncAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return nil, "", a.failure("fills")
}
func (a boundarySyncAdapter) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, a.failure("order")
}

func TestSyncAccountExternalErrorsCarryAccountIdentity(t *testing.T) {
	for _, stage := range []string{"open_orders", "positions", "snapshot", "fills", "order"} {
		t.Run(stage, func(t *testing.T) {
			db := openSyncStore(t)
			seedSyncAccount(t, db)
			if stage == "order" {
				require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
					return tx.CreateOrder(store.OrderRecord{
						SpaceID: "space-1", OrderID: "order-1", TradingAccountID: "account-1", ClientOrderID: "client-1",
						ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
						Quantity: "1", ReferencePrice: "100", ReferencePriceAt: 1000, OwnerType: "TARGET", OwnerID: "target-1",
						LogicalAccountID: "logical-1", RunnerID: "runner-1", State: "OPEN", FilledQuantity: "0", Version: 1,
					})
				}))
			}
			cause := errors.New("adapter plain error")
			s := &Service{Store: db, Fills: &consumer.Reducer{Store: db}, Adapters: syncAdapterSource{adapter: boundarySyncAdapter{syncAdapter: &syncAdapter{}, stage: stage, cause: cause}}}
			_, err := s.SyncAccount(context.Background(), "account-1")
			require.ErrorIs(t, err, cause)
			var accountErr *orderapp.AccountExecutionError
			require.ErrorAs(t, err, &accountErr)
			require.Equal(t, "account-1", accountErr.TradingAccountID)
			account, err := db.GetTradingAccountByID(context.Background(), "account-1")
			require.NoError(t, err)
			require.False(t, account.Ready)
			require.Equal(t, cause.Error(), account.LastError)
		})
	}
}

func TestSyncAccountFailureRetainsReadinessWriteFailure(t *testing.T) {
	db := openSyncStore(t)
	seedSyncAccount(t, db)
	dbErr := errors.New("readiness database failure")
	require.NoError(t, db.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:sync_boundary", func(query *gorm.DB) { query.AddError(dbErr) }))
	cause := errors.New("quote unavailable")
	s := &Service{Store: db, Fills: &consumer.Reducer{Store: db}, Adapters: syncAdapterSource{adapter: boundarySyncAdapter{syncAdapter: &syncAdapter{}, stage: "snapshot", cause: cause}}}
	_, err := s.SyncAccount(context.Background(), "account-1")
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, dbErr)
	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok)
	require.Len(t, joined.Unwrap(), 2)
	var accountErr *orderapp.AccountExecutionError
	require.ErrorAs(t, joined.Unwrap()[0], &accountErr)
	require.False(t, errors.As(joined.Unwrap()[1], &accountErr))
}

func TestSyncAccountPaperStorageErrorIsNotAccountDependency(t *testing.T) {
	db := openSyncStore(t)
	seedSyncAccount(t, db)
	cause := errors.New("paper ledger read failed")
	s := &Service{Store: db, Fills: &consumer.Reducer{Store: db}, Adapters: syncAdapterSource{adapter: boundarySyncAdapter{
		syncAdapter: &syncAdapter{}, stage: "snapshot", cause: paper.InfrastructureError{Err: cause},
	}}}
	_, err := s.SyncAccount(context.Background(), "account-1")
	require.ErrorIs(t, err, cause)
	require.True(t, paper.IsInfrastructureError(err))
	var accountErr *orderapp.AccountExecutionError
	require.False(t, errors.As(err, &accountErr))
}
