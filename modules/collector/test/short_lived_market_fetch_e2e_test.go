package test

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	marketdatahandler "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
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

type e2eGetter struct{}

func (e2eGetter) Get(_ context.Context, _ string, _ string, _ url.Values, result interface{}) error {
	return json.Unmarshal([]byte(`{"rc":0,"data":{"klines":["2026-08-31 09:30,10,10.5,11,9.5,100,1050"]}}`), result)
}

func (e2eGetter) GetStream(_ context.Context, _ string, _ string, _ url.Values, consume func(io.Reader) error) error {
	return consume(strings.NewReader(`{"data":{}}`))
}

type e2eStorage struct {
	mu      sync.Mutex
	commits int
	rows    int
	sources []string
}

func (s *e2eStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	s.rows += len(rows)
	return nil
}

func (s *e2eStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	s.rows += len(rows)
	s.sources = append(s.sources, source)
	return nil
}

func (s *e2eStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func (s *e2eStorage) snapshot() (commits, rows int, sources []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits, s.rows, append([]string(nil), s.sources...)
}

// TestShortLivedMarketFetchCompletionE2E proves the local durable boundary:
// a dispatched batch is executed once, its governed EventBus completion is
// ACKed, and the same SQLite transaction marks the stable task fresh.
func TestShortLivedMarketFetchCompletionE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Setenv("MOOX_SPACE_ID", "stock_cn")

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
	require.NoError(t, marketfetch.StartCompletionConsumer(ctx, "stock_cn", dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), metrics))

	now := time.Now().UTC()
	request := marketfetch.Request{
		BatchID: "e2e-batch", ScheduleID: "e2e-schedule", BatchKind: domain.BatchKindRealtime,
		SpaceID: "stock_cn", DatasetID: "stock_cn_kline", Frequency: "1m", Provider: "eastmoney", MarketType: "equity",
		Region: "ap-guangzhou", NodeID: "node-1",
		Items: []domain.CollectionItem{{TaskID: "task-1", SubjectID: "SH.600000", Symbol: "SH.600000", DataType: "kline", DatasetID: "stock_cn_kline", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}},
	}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, dbm.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: "stock_cn", TaskID: "task-1", RuleID: "rule-1", Provider: "eastmoney", MarketType: "equity",
		DataType: "kline", DatasetID: "stock_cn_kline", SubjectID: "SH.600000", Frequency: "1m",
	}}))
	batch := &domain.BatchInvocation{
		SpaceID: "stock_cn", BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		RuleID: "rule-1", DatasetID: "stock_cn_kline", Frequency: "1m", Region: request.Region, NodeID: request.NodeID,
		Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: 1,
		PlannedAt: &now, DeadlineAt: timePtr(now.Add(30 * time.Second)),
	}
	created, err := dbm.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	updated, err := dbm.FetchBatches().MarkDispatched(ctx, "stock_cn", request.BatchID, "cloud-request-1", now.Add(30*time.Second))
	require.NoError(t, err)
	require.True(t, updated)

	storage := &e2eStorage{}
	handler := marketdatahandler.NewHandler()
	handler.NewStorage = func(string, string, string) (marketfetch.KlineRowWriter, error) { return storage, nil }
	handler.NewGetter = func() markethttp.Getter { return e2eGetter{} }
	eventRaw, err := json.Marshal(map[string]interface{}{
		"action": "market_fetch", "request_id": "cloud-request-1", "storage_rpc_gateway_target": "fake-storage",
		"data": map[string]interface{}{
			"space_id": "stock_cn", "dataset_id": "stock_cn_kline", "market_id": "stock_cn", "instrument_type": "equity",
			"provider_id": "eastmoney", "source_id": "stock_cn_http", "batch_kind": "realtime", "schedule_id": request.ScheduleID, "frequency": "1m", "source_event_id": request.BatchID, "region": request.Region, "node_id": request.NodeID,
			"items": []map[string]string{{"task_id": "task-1", "subject_id": "SH.600000", "provider_symbol": "SH.600000"}},
		},
	})
	require.NoError(t, err)
	response, err := handler.HandleRequest(ctx, eventRaw)
	require.NoError(t, err)
	result, ok := response.(marketdatahandler.Response)
	require.True(t, ok)
	require.True(t, result.Success)
	require.Equal(t, 1, result.RowsWritten)
	commits, rows, sources := storage.snapshot()
	require.Equal(t, 1, commits)
	require.Equal(t, 1, rows)
	require.Len(t, sources, 1)
	require.Contains(t, sources[0], "e2e-batch:")

	require.Eventually(t, func() bool {
		current, getErr := dbm.FetchBatches().Get(ctx, "stock_cn", request.BatchID)
		return getErr == nil && current.Status == domain.BatchStatusSucceeded
	}, 5*time.Second, 20*time.Millisecond, "completion consumer did not persist terminal batch")
	instance, err := dbm.TaskInstances().Get(ctx, "stock_cn", "task-1")
	require.NoError(t, err)
	require.NotNil(t, instance.LastExecTime)
	require.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
	pending, err := dbm.FetchRetries().CountPending(ctx, "stock_cn", "stock_cn_kline", "1m")
	require.NoError(t, err)
	require.Zero(t, pending)

	require.Eventually(t, func() bool {
		info, infoErr := bus.JetStream().ConsumerInfo(events.MarketFetchBatchCompleted.Stream(), "collector-market-fetch-completion-v1-stock_cn")
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
