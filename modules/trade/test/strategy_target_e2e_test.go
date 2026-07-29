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
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTargetIntentConsumerPersistsOnlyLatestSequence(t *testing.T) {
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	var wakes atomic.Int32
	now := time.Now().UTC()
	options := eventconsumer.TargetOptions{
		Store: f.store, Wake: func() { wakes.Add(1) },
		Now: func() time.Time { return now },
	}

	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "target-2", 2, "0.02"),
		options,
	)
	require.Equal(t, jetstream.ACK, result.Decision)
	result = eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "target-stale", 1, "9"),
		options,
	)
	require.Equal(t, jetstream.ACK, result.Decision)
	result = eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "target-3", 3, "0.03"),
		options,
	)
	require.Equal(t, jetstream.ACK, result.Decision)

	current, err := f.store.GetTargetExecutionByBinding(
		context.Background(),
		testSpace,
		"binding-e2e",
	)
	require.NoError(t, err)
	require.Equal(t, "target-3", current.ExecutionID)
	require.Equal(t, uint64(3), current.CommandSequence)
	require.Equal(t, "0.03", current.Targets[0].TargetQuantity)
	require.Equal(t, int32(2), wakes.Load(), "stale command must not wake convergence")

	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now,
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:    func() time.Time { return now }, MaxChildNotional: shared.MustDecimal("1000000"),
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

	_, err = executor.Converge(context.Background(), testSpace, "binding-e2e")
	require.NoError(t, err)
	f.fake.mu.Lock()
	require.Len(t, f.fake.requests, 1, "active child order must suppress duplicate placement")
	f.fake.mu.Unlock()
	cancel()
	<-workerDone
}

func TestTargetConvergenceRejectsStaleReferenceBeforeExchangePlacement(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	result := eventconsumer.HandleTarget(
		context.Background(),
		targetDelivery(t, now, "stale-price-target", 1, "0.03"),
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }},
	)
	require.Equal(t, jetstream.ACK, result.Decision)
	f.fake.reference = exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: now.Add(-2 * time.Minute),
	}
	f.orders.Validator.Now = func() time.Time { return now }
	executor := targetapp.Executor{
		Store: f.store, Orders: f.orders,
		Prices: targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}},
		Now:    func() time.Time { return now }, MaxChildNotional: shared.MustDecimal("1000000"),
	}
	_, err := executor.Converge(context.Background(), testSpace, "binding-e2e")
	require.Error(t, err)
	require.True(t, errors.Is(err, orderdomain.ErrInvalidSpec))
	f.fake.mu.Lock()
	require.Empty(t, f.fake.requests)
	f.fake.mu.Unlock()
}

func TestTargetIntentRejectsExpiredNotAfter(t *testing.T) {
	now := time.Now().UTC()
	f := newFixture(t, exchange.MarketTypeSpot, newFakeExchange(exchange.MarketTypeSpot))
	delivery := targetDelivery(t, now, "expired-target", 1, "0.03")
	var envelope events.EventMessage
	require.NoError(t, proto.Unmarshal(delivery.RawData, &envelope))
	var payload tradeeventpb.TargetIntent
	require.NoError(t, proto.Unmarshal(envelope.Payload, &payload))
	payload.NotAfterUnixMs = now.Add(-time.Second).UnixMilli()
	var err error
	envelope.Payload, err = proto.Marshal(&payload)
	require.NoError(t, err)
	delivery.RawData, err = proto.Marshal(&envelope)
	require.NoError(t, err)

	result := eventconsumer.HandleTarget(
		context.Background(),
		delivery,
		eventconsumer.TargetOptions{Store: f.store, Now: func() time.Time { return now }},
	)
	require.Equal(t, jetstream.TERM, result.Decision)
	require.Error(t, result.Err)
	records, err := f.store.ListTargetExecutions(context.Background())
	require.NoError(t, err)
	require.Empty(t, records)
}

func targetDelivery(t *testing.T, now time.Time, executionID string, sequence uint64, quantity string) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		events.TradeTargetRequested,
		&tradeeventpb.TargetIntent{
			ExecutionId: executionID, StrategyRunId: "strategy-run-e2e",
			ExecutionBindingId: "binding-e2e", ExchangeAccountId: testAccount,
			DataRevision: "revision-e2e", CommandSequence: sequence,
			NotAfterUnixMs: now.Add(time.Minute).UnixMilli(),
			Targets: []*tradeeventpb.TargetPosition{{
				InstrumentId: "BTC-USDT", Symbol: testSymbol, TargetQuantity: quantity,
			}},
		},
		events.PublishOptions{
			EventID: executionID, OccurredAt: now,
			SpaceID: testSpace, SubjectID: "binding-e2e",
		},
	)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return &jetstream.Delivery{
		RawData: raw, Subject: encoded.Subject,
		RawMessageID: executionID, ContentType: events.ContentType,
	}
}
