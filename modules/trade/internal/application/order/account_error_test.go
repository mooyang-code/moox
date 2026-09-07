package order

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type errorBoundarySource struct{ adapter execution.ExecutionAdapter }

type contextCheckingSyncer struct{}

func (contextCheckingSyncer) SyncAccount(ctx context.Context, _ string) error { return ctx.Err() }

func TestSuccessfulMutationAfterDeadlineCanReconcile(t *testing.T) {
	for _, operation := range []string{"submit", "cancel", "recover_cancel"} {
		t.Run(operation, func(t *testing.T) {
			s, _, adapter := newTestService(t)
			s.Syncer = contextCheckingSyncer{}
			pending, err := s.Place(context.Background(), "space-1", testSpec(s.now()))
			require.NoError(t, err)
			if operation != "submit" {
				_, err = s.Submit(context.Background(), "space-1", string(pending.ID))
				require.NoError(t, err)
			}
			if operation == "recover_cancel" {
				_, err = s.Cancel(context.Background(), "space-1", string(pending.ID))
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if operation == "submit" {
				adapter.placeResult.Status = exchange.OrderStatusFilled
				adapter.placeHook = func() { <-ctx.Done() }
				_, err = s.Submit(ctx, "space-1", string(pending.ID))
			} else {
				adapter.cancelHook = func() { <-ctx.Done() }
				if operation == "cancel" {
					_, err = s.Cancel(ctx, "space-1", string(pending.ID))
				} else {
					_, err = s.RecoverCancel(ctx, "space-1", string(pending.ID))
				}
			}
			if operation == "recover_cancel" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestContextTerminationIsUncertainAfterExchangeMutation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		require.True(t, uncertainExchangeError(cause))
	}
}

func TestExchangeDeadlinePersistsUncertainOutcome(t *testing.T) {
	for _, operation := range []string{"submit", "cancel", "recover_cancel"} {
		t.Run(operation, func(t *testing.T) {
			s, db, adapter := newTestService(t)
			s.Syncer = &syncerStub{}
			pending, err := s.Place(context.Background(), "space-1", testSpec(s.now()))
			require.NoError(t, err)
			if operation != "submit" {
				_, err = s.Submit(context.Background(), "space-1", string(pending.ID))
				require.NoError(t, err)
			}
			if operation == "recover_cancel" {
				_, err = s.Cancel(context.Background(), "space-1", string(pending.ID))
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			cause := &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: context.DeadlineExceeded}
			if operation == "submit" {
				adapter.placeHook = func() { <-ctx.Done() }
				adapter.placeErr = cause
				_, err = s.Submit(ctx, "space-1", string(pending.ID))
			} else {
				adapter.cancelHook = func() { <-ctx.Done() }
				adapter.cancelErr = cause
				if operation == "cancel" {
					_, err = s.Cancel(ctx, "space-1", string(pending.ID))
				} else {
					_, err = s.RecoverCancel(ctx, "space-1", string(pending.ID))
				}
			}
			var accountErr *AccountExecutionError
			require.ErrorAs(t, err, &accountErr)
			require.ErrorIs(t, err, cause)
			record, err := db.GetOrder(context.Background(), "space-1", string(pending.ID))
			require.NoError(t, err)
			expected := "CANCEL_UNKNOWN"
			if operation == "submit" {
				expected = "SUBMIT_UNKNOWN"
			}
			require.Equal(t, expected, record.State)
			require.NotEqual(t, "0", record.RemainingReservedQuantity)
			if operation == "recover_cancel" {
				require.Equal(t, 1, adapter.getCalls)
			} else {
				require.Zero(t, adapter.getCalls)
			}
		})
	}
}

func (s errorBoundarySource) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
}

type errorBoundaryQuoteAdapter struct {
	*adapterStub
	err   error
	quote execution.MarketQuote
}

func (a errorBoundaryQuoteAdapter) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	return a.quote, a.err
}

type errorBoundarySnapshotAdapter struct {
	*adapterStub
	err error
}

func (a errorBoundarySnapshotAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, a.err
}

func TestPlacePaperQuoteErrorHasAccountIdentity(t *testing.T) {
	for _, cause := range []error{errors.New("quote source unavailable"), context.DeadlineExceeded} {
		s, _, adapter := newTestService(t)
		account := s.Validator.Accounts.(accountEligibilityStub).account
		account.ExecutionMode = exchange.ExecutionModePaper
		s.Validator.Accounts = accountEligibilityStub{account: account}
		s.Adapters = errorBoundarySource{adapter: errorBoundaryQuoteAdapter{adapterStub: adapter, err: cause}}
		_, err := s.Place(context.Background(), "space-1", testSpec(s.now()))
		var accountErr *AccountExecutionError
		require.ErrorAs(t, err, &accountErr)
		require.Equal(t, account.ID, accountErr.TradingAccountID)
		require.ErrorIs(t, err, cause)
		require.Equal(t, cause.Error(), err.Error())
	}
}

func TestSubmitAccountErrorOnlyAfterPersistedOutcome(t *testing.T) {
	for _, persistenceFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "database failure"}[persistenceFailure], func(t *testing.T) {
			s, db, adapter := newTestService(t)
			pending, err := s.Place(context.Background(), "space-1", testSpec(s.now()))
			require.NoError(t, err)
			cause := &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("venue rejected")}
			adapter.placeErr = cause
			dbErr := errors.New("injected update failure")
			if persistenceFailure {
				adapter.placeHook = func() {
					require.NoError(t, db.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:account_boundary", func(query *gorm.DB) { query.AddError(dbErr) }))
				}
			}
			_, err = s.Submit(context.Background(), "space-1", string(pending.ID))
			var accountErr *AccountExecutionError
			if persistenceFailure {
				require.ErrorIs(t, err, dbErr)
				require.False(t, errors.As(err, &accountErr))
			} else {
				require.ErrorAs(t, err, &accountErr)
				require.Equal(t, pending.Spec.TradingAccountID, accountErr.TradingAccountID)
				require.ErrorIs(t, err, cause)
				record, getErr := db.GetOrder(context.Background(), "space-1", string(pending.ID))
				require.NoError(t, getErr)
				require.Equal(t, "REJECTED", record.State)
				require.Equal(t, "0", record.RemainingReservedQuantity)
			}
		})
	}
}

func TestAccountExecutionErrorPreservesInfrastructure(t *testing.T) {
	cause := errors.New("sqlite failed")
	for _, err := range []error{paper.InfrastructureError{Err: cause}, &paper.InfrastructureError{Err: cause}} {
		got := accountExecutionError("account-1", "snapshot", err)
		var accountErr *AccountExecutionError
		require.False(t, errors.As(got, &accountErr))
		require.ErrorIs(t, got, cause)
	}
	require.NoError(t, accountExecutionError("account-1", "place", nil))
}

func TestPaperMarginSnapshotErrorBoundary(t *testing.T) {
	for _, infrastructure := range []bool{false, true} {
		s, _, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
		account := s.Validator.Accounts.(accountEligibilityStub).account
		account.ExecutionMode = exchange.ExecutionModePaper
		cause := errors.New("snapshot failed")
		var snapshotErr error = cause
		if infrastructure {
			snapshotErr = paper.InfrastructureError{Err: cause}
		}
		s.Adapters = errorBoundarySource{adapter: errorBoundarySnapshotAdapter{adapterStub: adapter, err: snapshotErr}}
		_, err := s.paperMarginAdjustment(context.Background(), Validation{Account: account})
		require.ErrorIs(t, err, cause)
		var accountErr *AccountExecutionError
		if infrastructure {
			require.False(t, errors.As(err, &accountErr))
		} else {
			require.ErrorAs(t, err, &accountErr)
			require.Equal(t, account.ID, accountErr.TradingAccountID)
			require.Equal(t, "snapshot", accountErr.Operation)
		}
	}
}

func TestRecoverCancelDoesNotHideSyncFailure(t *testing.T) {
	s, _, adapter := newTestService(t)
	syncer := &syncerStub{}
	s.Syncer = syncer
	ctx := context.Background()
	pending, err := s.Place(ctx, "space-1", testSpec(s.now()))
	require.NoError(t, err)
	_, err = s.Submit(ctx, "space-1", string(pending.ID))
	require.NoError(t, err)
	_, err = s.Cancel(ctx, "space-1", string(pending.ID))
	require.NoError(t, err)
	adapter.cancelErr = &exchange.Error{Kind: exchange.ErrorRejected}
	cause := errors.New("sync persistence failed")
	syncer.err = cause
	_, err = s.RecoverCancel(ctx, "space-1", string(pending.ID))
	require.ErrorIs(t, err, cause)
	_, accountBoundary := err.(*AccountExecutionError)
	require.False(t, accountBoundary, "shared sync error must remain outside the account boundary")
	require.ErrorIs(t, err, adapter.cancelErr)
}

func TestCancelAccountErrorOnlyAfterPersistedOutcome(t *testing.T) {
	for _, recoverCancel := range []bool{false, true} {
		for _, persistenceFailure := range []bool{false, true} {
			s, db, adapter := newTestService(t)
			s.Syncer = &syncerStub{}
			ctx := context.Background()
			pending, err := s.Place(ctx, "space-1", testSpec(s.now()))
			require.NoError(t, err)
			_, err = s.Submit(ctx, "space-1", string(pending.ID))
			require.NoError(t, err)
			if recoverCancel {
				_, err = s.Cancel(ctx, "space-1", string(pending.ID))
				require.NoError(t, err)
			}
			cause := &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: errors.New("venue timeout")}
			adapter.cancelErr = cause
			dbErr := errors.New("injected cancel update failure")
			if persistenceFailure {
				adapter.cancelHook = func() {
					require.NoError(t, db.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:cancel_boundary", func(query *gorm.DB) { query.AddError(dbErr) }))
				}
			}
			if recoverCancel {
				_, err = s.RecoverCancel(ctx, "space-1", string(pending.ID))
			} else {
				_, err = s.Cancel(ctx, "space-1", string(pending.ID))
			}
			var accountErr *AccountExecutionError
			if persistenceFailure {
				require.ErrorIs(t, err, dbErr)
				require.False(t, errors.As(err, &accountErr))
			} else {
				require.ErrorAs(t, err, &accountErr)
				require.Equal(t, pending.Spec.TradingAccountID, accountErr.TradingAccountID)
				require.ErrorIs(t, err, cause)
				record, getErr := db.GetOrder(ctx, "space-1", string(pending.ID))
				require.NoError(t, getErr)
				require.Equal(t, "CANCEL_UNKNOWN", record.State)
			}
		}
	}
}

func TestResolveUnknownAccountDependencyErrors(t *testing.T) {
	for _, fillsFailure := range []bool{false, true} {
		s, db, adapter := newTestService(t)
		ctx := context.Background()
		pending, err := s.Place(ctx, "space-1", testSpec(s.now()))
		require.NoError(t, err)
		setStoredOrderState(t, db, string(pending.ID), "SUBMITTING", s.now())
		cause := errors.New("venue lookup unavailable")
		adapter.getErr = cause
		if fillsFailure {
			adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
			adapter.fillsErr = cause
		}
		_, err = s.ResolveUnknown(ctx, "space-1", string(pending.ID))
		var accountErr *AccountExecutionError
		require.ErrorAs(t, err, &accountErr)
		require.Equal(t, pending.Spec.TradingAccountID, accountErr.TradingAccountID)
		require.ErrorIs(t, err, cause)
		record, getErr := db.GetOrder(ctx, "space-1", string(pending.ID))
		require.NoError(t, getErr)
		require.Equal(t, "SUBMIT_UNKNOWN", record.State)
	}
}
