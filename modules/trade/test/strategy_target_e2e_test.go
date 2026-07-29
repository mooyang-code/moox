package test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	testLogicalAccount = "logical-e2e"
	testRunner         = "runner-e2e"
)

func TestLogicalAccountTargetConsumerPersistsLatestAndWorkerConverges(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedLogicalAccount(t, f.store)
	var wakes atomic.Int32
	now := time.Now().UTC()
	options := eventconsumer.TargetOptions{
		Store: f.store, Wake: func() { wakes.Add(1) },
		Now: func() time.Time { return now },
	}

	for _, command := range []struct {
		id       string
		sequence int64
		quantity string
		decision jetstream.HandlerDecision
	}{
		{id: "target-2", sequence: 2, quantity: "0.02", decision: jetstream.ACK},
		{id: "target-stale", sequence: 1, quantity: "9", decision: jetstream.TERM},
		{id: "target-3", sequence: 3, quantity: "0.03", decision: jetstream.ACK},
	} {
		result := eventconsumer.HandleTarget(
			context.Background(),
			targetDelivery(t, now, command.id, command.sequence, command.quantity),
			options,
		)
		require.Equal(t, command.decision, result.Decision, result.Err)
	}

	current, err := f.store.GetLogicalAccountTarget(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "target-3", current.TargetID)
	require.Equal(t, uint64(3), current.CommandSequence)
	require.Equal(t, "0.03", current.Targets[0].Quantity)
	require.Equal(t, int32(2), wakes.Load(), "stale command must not wake convergence")

	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now,
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	worker := &traderuntime.TargetWorker{
		Store: f.store, Executor: executor, Interval: time.Hour,
		Now: func() time.Time { return now },
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = worker.Run(workerCtx)
	}()
	worker.Wake()
	require.Eventually(t, func() bool {
		f.fake.mu.Lock()
		defer f.fake.mu.Unlock()
		return len(f.fake.requests) == 1
	}, time.Second, 10*time.Millisecond)
	f.fake.mu.Lock()
	child := f.fake.requests[0]
	f.fake.mu.Unlock()
	require.Equal(t, exchange.OrderTypeMarket, child.OrderType)
	require.Equal(t, exchange.SideSell, child.Side)
	require.Equal(t, "9.97", child.Quantity.String())

	_, err = executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	f.fake.mu.Lock()
	require.Len(t, f.fake.requests, 1, "active child must suppress duplicate placement")
	f.fake.mu.Unlock()
	cancel()
	<-workerDone
}

func TestEmptyFullTargetClosesExistingPosition(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedLogicalAccount(t, f.store)
	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDeliveryWithTargets(t, now, "target-empty", 1, nil),
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }},
	)
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now,
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	converged, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "place", converged.Action)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Len(t, f.fake.requests, 1)
	require.Equal(t, exchange.SideSell, f.fake.requests[0].Side)
	require.Equal(t, "10", f.fake.requests[0].Quantity.String())
}

func TestLogicalAccountTargetRejectsStaleReferenceBeforePlacement(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	seedLogicalAccount(t, f.store)
	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "stale-price-target", 1, "0.03"),
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }},
	)
	require.Equal(t, jetstream.ACK, result.Decision, result.Err)
	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now.Add(-2 * time.Minute),
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices:           targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	_, err := executor.Converge(
		context.Background(),
		testSpace,
		testLogicalAccount,
	)
	require.Error(t, err)
	require.True(t, errors.Is(err, orderdomain.ErrInvalidSpec))
	f.fake.mu.Lock()
	require.Empty(t, f.fake.requests)
	f.fake.mu.Unlock()
}

func seedLogicalAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			Name: "E2E logical account", OwnerRunnerID: testRunner,
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			ExchangeAccountID: testAccount, Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(
			testSpace,
			testLogicalAccount,
			"ACTIVE",
			"",
		)
	}))
}

func targetDelivery(
	t *testing.T,
	now time.Time,
	targetID string,
	sequence int64,
	quantity string,
) *jetstream.Delivery {
	t.Helper()
	return targetDeliveryWithTargets(
		t,
		now,
		targetID,
		sequence,
		[]*tradeeventpb.InstrumentTarget{{
			InstrumentId: "BTC-USDT",
			Quantity:     quantity,
		}},
	)
}

func targetDeliveryWithTargets(
	t *testing.T,
	now time.Time,
	targetID string,
	sequence int64,
	targets []*tradeeventpb.InstrumentTarget,
) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		events.LogicalAccountTargetRequested,
		&tradeeventpb.LogicalAccountTargetRequested{
			TargetId: targetID, RunnerId: testRunner,
			LogicalAccountId: testLogicalAccount,
			CommandSequence:  sequence, Targets: targets,
		},
		events.PublishOptions{
			EventID: targetID, OccurredAt: now,
			SpaceID: testSpace, SubjectID: testLogicalAccount,
		},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, Subject: encoded.Subject,
		RawMessageID: targetID, ContentType: events.ContentType,
	}
}
