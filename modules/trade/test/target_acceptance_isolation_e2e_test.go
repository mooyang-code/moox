package test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type blockedTargetPrice struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockedTargetPrice) LatestPrice(ctx context.Context, _, _ string) (targetapp.Quote, error) {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-ctx.Done():
		return targetapp.Quote{}, ctx.Err()
	case <-p.release:
		return targetapp.Quote{}, context.Canceled
	}
}

func TestBlockedTargetQuoteDoesNotBlockOtherAccountAcceptance(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := eventconsumer.TargetOptions{
		Store: f.store, Now: func() time.Time { return now },
		WeightResolver: testTargetWeightResolver{},
	}
	accepted := eventconsumer.HandleTarget(ctx, targetDelivery(t, now, "blocked-target", 1, "0.03"), opts)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: "logical-other", Name: "Other logical account",
			OwnerInstanceID: "instance-other", OwnerSessionID: "session-other",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	prices := &blockedTargetPrice{entered: make(chan struct{}), release: make(chan struct{})}
	worker := &traderuntime.TargetWorker{
		Store: f.store, Executor: &targetapp.Executor{
			Store: f.store, Orders: f.orders, Prices: prices, Now: func() time.Time { return now },
		}, Interval: time.Hour,
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-prices.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("production target executor did not reach price lookup")
	}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.LogicalAccountTargetWeightRequested,
		&tradeeventpb.LogicalAccountTargetWeightRequested{
			TargetId: "other-target", LogicalAccountId: "logical-other",
			InstanceId: "instance-other", SessionId: "session-other", StrategyId: "strategy-other",
			BarEndTime: timestamppb.New(now), EffectiveAt: timestamppb.New(now),
			ValidUntil: timestamppb.New(now.Add(time.Hour)),
		}, events.PublishOptions{EventID: "other-target", OccurredAt: now, SpaceID: testSpace, SubjectID: "logical-other"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	result := make(chan jetstream.HandlerResult, 1)
	go func() {
		result <- eventconsumer.HandleTarget(ctx, &jetstream.Delivery{
			RawData: raw, Subject: encoded.Subject, RawMessageID: "other-target", ContentType: events.ContentType,
		}, opts)
	}()
	t.Cleanup(func() { cancel(); close(prices.release) })
	select {
	case actual := <-result:
		require.Equal(t, jetstream.ACK, actual.Decision, actual.Err)
	case <-time.After(time.Second):
		cancel()
		<-result
		t.Fatal("account B acceptance waited for account A's quote")
	}
	current, err := f.store.GetLogicalAccountTarget(ctx, testSpace, "logical-other")
	require.NoError(t, err)
	require.Equal(t, "other-target", current.TargetID)
	_, err = f.store.GetTargetReceipt(ctx, testSpace, "other-target")
	require.NoError(t, err, "ACK must follow durable receipt and target acceptance")
}

func TestExpiredTargetWorkerRemainsReadyWithoutTrading(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	opts := eventconsumer.TargetOptions{
		Store: f.store, Now: func() time.Time { return now }, WeightResolver: testTargetWeightResolver{},
	}
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "expiring-target", 1, "0.03"), opts)
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	expiredTime := now.Add(2 * time.Hour)
	prices := &blockedTargetPrice{entered: make(chan struct{}), release: make(chan struct{})}
	worker := &traderuntime.TargetWorker{
		Store: f.store, Executor: &targetapp.Executor{
			Store: f.store, Orders: f.orders, Prices: prices, Now: func() time.Time { return expiredTime },
		}, Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, 2*time.Second, 10*time.Millisecond)
	current, err := f.store.GetLogicalAccountTarget(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, current.Status)
	select {
	case <-prices.entered:
		t.Fatal("expired target requested a quote")
	default:
	}
	f.fake.mu.Lock()
	requests := len(f.fake.requests)
	f.fake.mu.Unlock()
	require.Zero(t, requests, "expiry must not trade or flatten existing holdings")
	active, err := f.store.ListLogicalAccountTargets(ctx, targetapp.StatusPending, targetapp.StatusConverging, targetapp.StatusBlocked, targetapp.StatusConverged)
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestTargetWorkerExchangeFailureRemainsAccountDiagnostic(t *testing.T) {
	for _, kind := range []exchange.ErrorKind{exchange.ErrorRejected, exchange.ErrorTransportUnknown, exchange.ErrorRateLimited} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
			seedTargetPaperHoldings(t, f)
			seedLogicalAccount(t, f.store)
			now := time.Now().UTC()
			f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
			f.fake.placeErr = &exchange.Error{Kind: kind, Err: errors.New("exchange unavailable")}
			f.orders.Now = func() time.Time { return now }
			f.orders.Validator.Now = f.orders.Now
			opts := eventconsumer.TargetOptions{Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{}}
			result := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "exchange-failure-target", 1, "0.03"), opts)
			require.Equal(t, jetstream.ACK, result.Decision, result.Err)
			worker := &traderuntime.TargetWorker{
				Store: f.store, Executor: &targetapp.Executor{
					Store: f.store, Orders: f.orders, Now: f.orders.Now,
					Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
				}, Interval: time.Hour,
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- worker.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			require.Eventually(t, func() bool {
				snapshot := worker.Snapshot()
				return snapshot.Ready && len(snapshot.TargetErrors) == 1
			}, 2*time.Second, 10*time.Millisecond)
			require.Empty(t, worker.Snapshot().LastError)
			orders, _, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
			require.NoError(t, err)
			require.Len(t, orders, 1, "%+v", worker.Snapshot().TargetErrors)
			if kind == exchange.ErrorRejected {
				require.Equal(t, "REJECTED", orders[0].State)
				require.Equal(t, "0", orders[0].RemainingReservedQuantity)
			} else {
				require.Equal(t, "SUBMIT_UNKNOWN", orders[0].State)
				require.NotEqual(t, "0", orders[0].RemainingReservedQuantity)
			}
		})
	}
}

type targetOrdersAfterPlace struct {
	*orderapp.Service
	afterPlace func()
}

func (s targetOrdersAfterPlace) Place(ctx context.Context, space string, spec orderdomain.OrderSpec) (orderdomain.Order, error) {
	order, err := s.Service.Place(ctx, space, spec)
	if err == nil {
		s.afterPlace()
	}
	return order, err
}

type expiringSubmitAdapterSource struct {
	adapter execution.ExecutionAdapter
	calls   int
	expire  func()
}

func (s *expiringSubmitAdapterSource) Adapter(string) (execution.ExecutionAdapter, error) {
	s.calls++
	if s.calls == 2 {
		s.expire()
	}
	return s.adapter, nil
}

func TestTargetExpiresAfterRealPlaceBeforeSubmit(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	currentTime := now
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return currentTime }
	f.orders.Validator.Now = f.orders.Now
	result := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "expiry-after-place", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
	executor := &targetapp.Executor{
		Store: f.store, Now: f.orders.Now,
		Orders: targetOrdersAfterPlace{Service: f.orders, afterPlace: func() { currentTime = now.Add(2 * time.Hour) }},
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
	}
	actual, err := executor.Converge(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, actual.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, target.Status)
	orders, _, err := f.store.ListOrders(context.Background(), testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "PENDING", orders[0].State)
	require.Zero(t, orders[0].SubmittedAt)
	require.NotEqual(t, "0", orders[0].RemainingReservedQuantity, "expiry does not implicitly discard orders or release reservations")
	require.Empty(t, f.fake.requests, "OrderService must reject expiry before any POST")
}

func TestTargetExpiresInsideRealSubmitBeforeAdapterPost(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	currentTime := now
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return currentTime }
	f.orders.Validator.Now = f.orders.Now
	source := &expiringSubmitAdapterSource{
		adapter: f.adapter,
		expire:  func() { currentTime = now.Add(2 * time.Hour) },
	}
	f.orders.Adapters = source
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "expiry-inside-submit", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders, Now: f.orders.Now,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
	}
	actual, err := executor.Converge(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, actual.Status)
	require.Equal(t, 2, source.calls, "expiry must happen after Submit has begun")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, target.Status)
	orders, _, err := f.store.ListOrders(context.Background(), testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "PENDING", orders[0].State)
	require.Zero(t, orders[0].SubmittedAt)
	require.NotEqual(t, "0", orders[0].RemainingReservedQuantity)
	require.Empty(t, f.fake.requests, "expiry inside Submit must be rejected before any adapter POST")

	again, err := executor.Converge(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, again.Status)
	require.Empty(t, f.fake.requests, "an expired target must not retry the retained pending order")
}

func TestTargetExpiresWhilePersistingSubmittingBeforeAdapterPost(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	currentTime := now
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return currentTime }
	f.orders.Validator.Now = f.orders.Now
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "expiry-during-submitting-write", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)

	var expireOnce sync.Once
	require.NoError(t, f.store.DBForTest().Callback().Raw().After("gorm:raw").Register("test:expire_after_submitting_write", func(tx *gorm.DB) {
		sql := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
		if strings.HasPrefix(sql, "UPDATE t_trade_orders SET c_exchange_order_id") {
			expireOnce.Do(func() { currentTime = now.Add(2 * time.Hour) })
		}
	}))
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders, Now: f.orders.Now,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
	}
	actual, err := executor.Converge(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, actual.Status)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, target.Status)
	orders, _, err := f.store.ListOrders(context.Background(), testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "PENDING", orders[0].State)
	require.Zero(t, orders[0].SubmittedAt)
	require.NotEqual(t, "0", orders[0].RemainingReservedQuantity)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Empty(t, f.fake.requests, "expiry during the SUBMITTING write must still prevent adapter POST")
	require.Zero(t, f.fake.lookupCalls, "a locally aborted submit is known unsent, not SUBMIT_UNKNOWN")
}

func TestTargetExpiryAbortPersistenceFailureIsGlobalAndNeverPosts(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	currentTime := now
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return currentTime }
	f.orders.Validator.Now = f.orders.Now
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "expiry-abort-write-failure", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)

	dbErr := errors.New("injected abort persistence failure")
	var expireOnce sync.Once
	callbacks := f.store.DBForTest().Callback().Raw()
	require.NoError(t, callbacks.Before("gorm:raw").Register("test:fail_expiry_abort_write", func(tx *gorm.DB) {
		sql := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
		if strings.HasPrefix(sql, "UPDATE t_trade_orders SET c_exchange_order_id") &&
			len(tx.Statement.Vars) > 1 && tx.Statement.Vars[1] == "PENDING" {
			tx.AddError(dbErr)
		}
	}))
	require.NoError(t, callbacks.After("gorm:raw").Register("test:expire_before_abort_write", func(tx *gorm.DB) {
		sql := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
		if strings.HasPrefix(sql, "UPDATE t_trade_orders SET c_exchange_order_id") &&
			len(tx.Statement.Vars) > 1 && tx.Statement.Vars[1] == "SUBMITTING" {
			expireOnce.Do(func() { currentTime = now.Add(2 * time.Hour) })
		}
	}))
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders, Now: f.orders.Now,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
	}
	_, err := executor.Converge(context.Background(), testSpace, testLogicalAccount)
	require.ErrorIs(t, err, dbErr)
	require.ErrorIs(t, err, orderapp.ErrTargetExpired)
	var accountErr *targetapp.AccountError
	require.False(t, errors.As(err, &accountErr), "an abort write failure is shared persistence health, not an account diagnostic")
	target, getErr := f.store.GetLogicalAccountTarget(context.Background(), testSpace, testLogicalAccount)
	require.NoError(t, getErr)
	require.Equal(t, targetapp.StatusPending, target.Status, "joined persistence failure must not be hidden by expiry handling")
	orders, _, getErr := f.store.ListOrders(context.Background(), testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, getErr)
	require.Len(t, orders, 1)
	require.Equal(t, "SUBMITTING", orders[0].State, "failed CAS preserves the last durable state for explicit recovery")
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Empty(t, f.fake.requests)
	require.Zero(t, f.fake.lookupCalls, "the pre-POST abort path must not invent an exchange lookup")
}

func TestCanceledTargetSubmitRestoresKnownUnsentPendingOrder(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = f.orders.Now
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "cancel-before-post", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	submittingWritten := false
	require.NoError(t, f.store.DBForTest().Callback().Raw().After("gorm:raw").Register("test:submitting_written", func(tx *gorm.DB) {
		sql := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
		if strings.HasPrefix(sql, "UPDATE t_trade_orders SET c_exchange_order_id") && len(tx.Statement.Vars) > 1 && tx.Statement.Vars[1] == "SUBMITTING" {
			submittingWritten = true
		}
	}))
	// This read follows the successful SUBMITTING transaction commit.
	require.NoError(t, f.store.DBForTest().Callback().Query().Before("gorm:query").Register("test:cancel_final_authorization", func(tx *gorm.DB) {
		if submittingWritten {
			cancel()
		}
	}))
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Now: f.orders.Now,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}}}
	_, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, submittingWritten)
	orders, _, err := f.store.ListOrders(context.Background(), testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "PENDING", orders[0].State)
	require.Zero(t, orders[0].SubmittedAt)
	require.NotEqual(t, "0", orders[0].RemainingReservedQuantity)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Empty(t, f.fake.requests)
	require.Zero(t, f.fake.lookupCalls)
}

func TestTargetWorkerPaperExecutableQuoteFailureRecovers(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedTargetPaperHoldings(t, f)
	seedLogicalAccount(t, f.store)
	now := time.Now().UTC()
	f.fake.reference = exchange.ReferencePrice{Price: shared.MustDecimal("50000"), UpdatedAt: now}
	f.fake.quoteErr = errors.New("executable bid/ask unavailable")
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = f.orders.Now
	accepted := eventconsumer.HandleTarget(context.Background(), targetDelivery(t, now, "second-quote-failure", 1, "0.03"), eventconsumer.TargetOptions{
		Store: f.store, Now: f.orders.Now, WeightResolver: testTargetWeightResolver{},
	})
	require.Equal(t, jetstream.ACK, accepted.Decision, accepted.Err)
	worker := &traderuntime.TargetWorker{
		Store: f.store, Executor: &targetapp.Executor{
			Store: f.store, Orders: f.orders, Now: f.orders.Now,
			Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		}, Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		snapshot := worker.Snapshot()
		return snapshot.Ready && len(snapshot.TargetErrors) == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, testAccount, worker.Snapshot().TargetErrors[0].TradingAccountID)
	orders, _, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{LogicalAccountID: testLogicalAccount, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, orders)
	f.fake.mu.Lock()
	require.Zero(t, f.fake.placeCalls)
	f.fake.quoteErr = nil
	f.fake.mu.Unlock()
	worker.Wake()
	require.Eventually(t, func() bool {
		f.fake.mu.Lock()
		calls := f.fake.placeCalls
		f.fake.mu.Unlock()
		return calls == 1 && worker.Snapshot().Ready && len(worker.Snapshot().TargetErrors) == 0
	}, 2*time.Second, 10*time.Millisecond)
}
