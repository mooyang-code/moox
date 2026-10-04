package test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type deadlineOpenOrdersAdapter struct{ execution.ExecutionAdapter }

type deadlineSuccessfulCancelAdapter struct{ execution.ExecutionAdapter }

func (a deadlineSuccessfulCancelAdapter) CancelOrder(ctx context.Context, symbol shared.ExchangeSymbol, id string) (exchange.Order, error) {
	<-ctx.Done()
	return a.ExecutionAdapter.CancelOrder(context.WithoutCancel(ctx), symbol, id)
}

func (a deadlineOpenOrdersAdapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTargetWorkerCancelSyncErrorKeepsCorrectReadinessScope(t *testing.T) {
	for _, mode := range []string{"account dependency", "readiness persistence", "real deadline", "successful cancel at deadline"} {
		t.Run(mode, func(t *testing.T) {
			writeFailure := mode == "readiness persistence"
			f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
			seedTargetPaperHoldings(t, f)
			seedLogicalAccount(t, f.store)
			now := time.Now().UTC()
			f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
			f.orders.Now = func() time.Time { return now }
			f.orders.Validator.Now = f.orders.Now
			opts := eventconsumer.TargetOptions{Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{}}
			executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Now: f.orders.Now,
				Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}}}
			ctx := context.Background()
			accepted := eventconsumer.HandleTarget(ctx, targetDelivery(t, now, "before-sync-failure", 1, "0.03"), opts)
			require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
			result, err := executor.Converge(ctx, testSpace, testLogicalAccount)
			require.NoError(t, err)
			require.Equal(t, "place", result.Action)
			accepted = eventconsumer.HandleTarget(ctx, targetDelivery(t, now, "after-sync-failure", 2, "0.04"), opts)
			require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
			f.fake.openOrdersErr = errors.New("sync venue unavailable")
			if mode == "successful cancel at deadline" {
				f.fake.openOrdersErr = nil
				f.orders.Adapters = adapterSource{adapter: deadlineSuccessfulCancelAdapter{ExecutionAdapter: f.adapter}}
			}
			if mode == "real deadline" {
				f.sync.Adapters = adapterSource{adapter: deadlineOpenOrdersAdapter{ExecutionAdapter: f.adapter}}
			}
			if writeFailure {
				require.NoError(t, f.store.DBForTest().Callback().Raw().Before("gorm:raw").Register("test:sync_readiness_failure", func(query *gorm.DB) {
					sql := query.Statement.SQL.String()
					if strings.Contains(sql, "UPDATE t_trading_accounts") && strings.Contains(sql, "c_ready") {
						query.AddError(errors.New("readiness write failed"))
					}
				}))
			}
			worker := &traderuntime.TargetWorker{Store: f.store, Executor: executor, Interval: time.Hour, ConvergeTimeout: time.Second}
			workerCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- worker.Run(workerCtx) }()
			t.Cleanup(func() { cancel(); <-done })
			require.Eventually(t, func() bool {
				snapshot := worker.Snapshot()
				if writeFailure {
					return !snapshot.Ready && strings.Contains(snapshot.LastError, "readiness write failed")
				}
				if mode == "successful cancel at deadline" {
					return snapshot.Ready && len(snapshot.TargetErrors) == 0
				}
				return snapshot.Ready && len(snapshot.TargetErrors) == 1 && snapshot.TargetErrors[0].TradingAccountID == testAccount
			}, 5*time.Second, 10*time.Millisecond)
			f.fake.mu.Lock()
			canceled := 0
			for _, order := range f.fake.orders {
				if order.Status == exchange.OrderStatusCanceled {
					canceled++
				}
			}
			f.fake.mu.Unlock()
			require.Equal(t, 1, canceled, "real target -> order Cancel -> account sync path must run")
			if !writeFailure {
				require.Empty(t, worker.Snapshot().LastError)
			}
		})
	}
}
