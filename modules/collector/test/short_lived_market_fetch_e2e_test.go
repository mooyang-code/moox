package test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type e2eKlines struct{}

func (e2eKlines) FetchRealtimeRows(context.Context, *sources.CollectParams, int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	return []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "bars"}}}, time.Now().UTC(), nil
}

type e2eSymbols struct{}

func (e2eSymbols) FetchSymbolSnapshot(context.Context, *sources.CollectParams) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	return nil, nil, "", nil
}

type e2eStorage struct {
	mu      sync.Mutex
	commits int
}

func (s *e2eStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	return nil
}

func (s *e2eStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func (s *e2eStorage) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

// TestShortLivedMarketFetchCompletionE2E proves the local durable boundary:
// a dispatched batch is executed once, its governed EventBus completion is
// ACKed, and the same SQLite transaction marks the stable task fresh.
func TestShortLivedMarketFetchCompletionE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bus := testkit.Start(t)
	bus.AddStream(t, &nats.StreamConfig{
		Name:      events.MarketFetchBatchCompleted.Stream(),
		Subjects:  []string{"moox.market.fetch.batch.completed.v1.>"},
		Storage:   nats.MemoryStorage,
		Retention: nats.LimitsPolicy,
	})
	t.Setenv("MOOX_EVENTBUS_NATS_URL", bus.URL())

	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(collectorschema.AllSQL()))

	metrics := marketfetch.NewMetrics(prometheus.NewRegistry())
	require.NoError(t, marketfetch.StartCompletionConsumer(ctx, "crypto", dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), metrics))

	now := time.Now().UTC()
	request := marketfetch.Request{
		BatchID: "e2e-batch", ScheduleID: "e2e-schedule", BatchKind: domain.BatchKindRealtime,
		SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Provider: "binance", MarketType: "spot",
		Region: "ap-guangzhou", NodeID: "node-1",
		Items: []domain.CollectionItem{{TaskID: "task-1", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DataType: "kline", DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}},
	}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, dbm.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Provider: "binance", MarketType: "spot",
		DataType: "kline", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m",
	}}))
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		RuleID: "rule-1", DatasetID: "bars", Frequency: "1m", Region: request.Region, NodeID: request.NodeID,
		Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: 1,
		PlannedAt: &now, DeadlineAt: timePtr(now.Add(30 * time.Second)),
	}
	created, err := dbm.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	updated, err := dbm.FetchBatches().MarkDispatched(ctx, "crypto", request.BatchID, "cloud-request-1", now.Add(30*time.Second))
	require.NoError(t, err)
	require.True(t, updated)

	storage := &e2eStorage{}
	executor := &marketfetch.Executor{Klines: e2eKlines{}, Symbols: e2eSymbols{}, Storage: storage}
	payload, err := executor.Execute(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	require.Equal(t, 1, storage.commitCount())

	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{bus.URL()}, Name: "market-fetch-e2e-publisher"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	_, err = publisher.Publish(ctx, events.MarketFetchBatchCompleted, payload, events.PublishOptions{
		EventID: request.BatchID, OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "bars",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		current, getErr := dbm.FetchBatches().Get(ctx, "crypto", request.BatchID)
		return getErr == nil && current.Status == domain.BatchStatusSucceeded
	}, 5*time.Second, 20*time.Millisecond, "completion consumer did not persist terminal batch")
	instance, err := dbm.TaskInstances().Get(ctx, "crypto", "task-1")
	require.NoError(t, err)
	require.NotNil(t, instance.LastExecTime)
	require.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
	pending, err := dbm.FetchRetries().CountPending(ctx, "crypto", "bars", "1m")
	require.NoError(t, err)
	require.Zero(t, pending)

	require.Eventually(t, func() bool {
		info, infoErr := bus.JetStream().ConsumerInfo(events.MarketFetchBatchCompleted.Stream(), "collector-market-fetch-completion-v1-crypto")
		return infoErr == nil && info.NumAckPending == 0 && info.NumPending == 0
	}, 5*time.Second, 20*time.Millisecond, "completion event was not ACKed")
}

func TestShortLivedMarketFetchCompletionConsumerIsolatedBySpace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bus := testkit.Start(t)
	bus.AddStream(t, &nats.StreamConfig{
		Name: events.MarketFetchBatchCompleted.Stream(), Subjects: []string{"moox.market.fetch.batch.completed.v1.>"},
		Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy,
	})
	t.Setenv("MOOX_EVENTBUS_NATS_URL", bus.URL())

	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisherClient, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{bus.URL()}, Name: "market-fetch-space-isolation-publisher"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = publisherClient.Close() })
	publisher, err := events.NewPublisher(publisherClient, registry)
	require.NoError(t, err)

	stores := map[string]*store.Store{}
	for _, spaceID := range []string{"crypto", "research"} {
		dbm, openErr := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), spaceID+".db")})
		require.NoError(t, openErr)
		require.NoError(t, dbm.ApplySchema(collectorschema.AllSQL()))
		stores[spaceID] = dbm
		t.Cleanup(func() { _ = dbm.Close() })
		require.NoError(t, marketfetch.StartCompletionConsumer(ctx, spaceID, dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), marketfetch.NewMetrics(prometheus.NewRegistry())))
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: "batch-" + spaceID, ScheduleID: "schedule-" + spaceID, BatchKind: domain.BatchKindRealtime, DatasetID: "bars", Frequency: "1m", Region: "ap-guangzhou", NodeID: "node-1", Status: domain.BatchStatusPlanned}
		created, createErr := dbm.FetchBatches().CreatePlanned(ctx, batch)
		require.NoError(t, createErr)
		require.True(t, created)
	}

	for _, spaceID := range []string{"crypto", "research"} {
		_, publishErr := publisher.Publish(ctx, events.MarketFetchBatchCompleted, &marketfetchpb.MarketFetchBatchCompleted{
			BatchId: "batch-" + spaceID, ScheduleId: "schedule-" + spaceID, BatchKind: string(domain.BatchKindRealtime), DatasetId: "bars", Frequency: "1m", NodeId: "node-1", Status: string(domain.BatchStatusSucceeded), CompletedAt: timestamppb.Now(),
		}, events.PublishOptions{EventID: "batch-" + spaceID, OccurredAt: time.Now().UTC(), SpaceID: spaceID, SubjectID: "bars"})
		require.NoError(t, publishErr)
	}

	for _, spaceID := range []string{"crypto", "research"} {
		spaceID := spaceID
		require.Eventually(t, func() bool {
			batch, getErr := stores[spaceID].FetchBatches().Get(ctx, spaceID, "batch-"+spaceID)
			return getErr == nil && batch.Status == domain.BatchStatusSucceeded
		}, 5*time.Second, 20*time.Millisecond, "completion was not persisted for space %s", spaceID)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
